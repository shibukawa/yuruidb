package recordstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"sort"
	"strings"
	"testing"
)

type counting struct {
	data                 map[string]Item
	reads, writes, scans int
	unknown, unreadable  bool
	afterWrite           func()
}

func (c *counting) Read(ctx context.Context, key string) (Item, error) {
	c.reads++
	if c.unreadable {
		return Item{}, errors.New("read unavailable")
	}
	item, ok := c.data[key]
	if !ok {
		return item, yuruidb.ErrNotFound
	}
	return item, nil
}
func (c *counting) Write(ctx context.Context, key string, b []byte, token string) error {
	_, err := c.WriteItem(ctx, key, b, token)
	return err
}
func (c *counting) WriteItem(ctx context.Context, key string, b []byte, token string) (string, error) {
	c.writes++
	old, ok := c.data[key]
	if ok && old.Token != token || !ok && token != "" {
		return "", yuruidb.ErrConflict
	}
	next := fmt.Sprint(c.writes)
	c.data[key] = Item{Data: append([]byte(nil), b...), Token: next}
	if c.afterWrite != nil {
		c.afterWrite()
	}
	if c.unknown {
		return "", errors.Join(yuruidb.ErrOutcomeUnknown, yuruidb.ErrConflict)
	}
	return next, nil
}
func (c *counting) Delete(ctx context.Context, key, expected string) error {
	old, ok := c.data[key]
	if !ok || old.Token != expected {
		return yuruidb.ErrConflict
	}
	delete(c.data, key)
	return nil
}
func (c *counting) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	keys := []string{}
	for k := range c.data {
		if k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more, nil
}
func (c *counting) Scan(ctx context.Context, after string, n int) ([]Entry, bool, error) {
	c.scans++
	keys, more, err := c.Keys(ctx, after, n)
	entries := make([]Entry, len(keys))
	for i, k := range keys {
		entries[i] = Entry{Key: k, Item: c.data[k]}
	}
	return entries, more, err
}
func (c *counting) Close() error { return nil }
func fixture(t *testing.T) (*Store, *counting) {
	t.Helper()
	c := &counting{data: map[string]Item{}}
	s, err := New(c, "items", "fixture", yuruidb.Capabilities{ConditionalMutations: true, StrongReads: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}
func TestRoundTripsAndDeletedPopulation(t *testing.T) {
	ctx := context.Background()
	s, c := fixture(t)
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("job-%04d", i)
		r, err := s.Insert(ctx, id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		if i < 1990 {
			if err = s.Delete(ctx, id, r.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c.reads != 0 || c.writes != 2000 || len(c.data) != 10 {
		t.Fatalf("insert/delete reads=%d writes=%d entries=%d", c.reads, c.writes, len(c.data))
	}
	page, err := s.List(ctx, yuruidb.ListOptions{})
	if err != nil || len(page.Records) != 10 || c.reads != 0 || c.scans != 1 {
		t.Fatalf("list records=%d reads=%d scans=%d err=%v", len(page.Records), c.reads, c.scans, err)
	}
	r := page.Records[0]
	next, err := s.Update(ctx, r.ID, []byte("next"), r.Revision)
	if err != nil || c.reads != 0 {
		t.Fatalf("update reads=%d err=%v", c.reads, err)
	}
	if err = s.Delete(ctx, r.ID, next.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, r.ID, []byte("recreated")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, r.ID, nil, next.Revision); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatalf("ABA update: %v", err)
	}
	if err = s.Delete(ctx, r.ID, next.Revision); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatalf("ABA delete: %v", err)
	}
}
func TestResponseLossAndReconciliation(t *testing.T) {
	ctx := context.Background()
	s, c := fixture(t)
	r, err := s.Insert(ctx, "job", []byte("initial"))
	if err != nil {
		t.Fatal(err)
	}
	c.unknown = true
	next, err := s.Update(ctx, "job", []byte("committed"), r.Revision)
	if err != nil || string(next.Value) != "committed" {
		t.Fatalf("automatic reconciliation: %+v %v", next, err)
	}
	c.unreadable = true
	_, err = s.Update(ctx, "job", []byte("unreadable-response"), next.Revision)
	var unknown *yuruidb.MutationError
	if !errors.Is(err, yuruidb.ErrOutcomeUnknown) || errors.Is(err, yuruidb.ErrConflict) || !errors.As(err, &unknown) || unknown.Operation != "update" || unknown.Nonce == "" || unknown.ExpectedRevision != next.Revision {
		t.Fatalf("unknown outcome: %+v", err)
	}
	c.unreadable = false
	got, readErr := s.Get(ctx, "job")
	if readErr != nil || !unknown.MatchesRevision(got.Revision) {
		t.Fatalf("caller cannot reconcile: %+v %v", got, readErr)
	}
	c.unknown = false
	later, err := s.Update(ctx, "job", []byte("later"), got.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.MatchesRevision(later.Revision) {
		t.Fatal("superseding write matched attempted revision")
	}
}

type basicTransport struct{ Transport }

func TestTombstoneFallbackAndSafeCleanup(t *testing.T) {
	ctx := context.Background()
	s, c := fixture(t)
	fallback, err := New(basicTransport{c}, "items", "fixture", s.caps)
	if err != nil {
		t.Fatal(err)
	}
	r, err := fallback.Insert(ctx, "job", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = fallback.Delete(ctx, "job", r.Revision); err != nil {
		t.Fatal(err)
	}
	e, err := Decode(c.data["items/job"].Data, "job")
	if err != nil || !e.Deleted {
		t.Fatalf("fallback tombstone: %+v %v", e, err)
	}
	count, err := s.PurgeTombstones(ctx)
	if err != nil || count != 1 || len(c.data) != 0 {
		t.Fatalf("purge: %d %v", count, err)
	}
	// Recreate with CAS before a delayed cleanup delete reaches the backend.
	rev, _ := yuruidb.NewRevision()
	b, _ := json.Marshal(Envelope{ID: "job", Revision: rev, Deleted: true})
	token, err := c.WriteItem(ctx, "items/job", b, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, "job", []byte("live")); err != nil {
		t.Fatal(err)
	}
	if err = c.Delete(ctx, "items/job", token); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatalf("cleanup deleted recreation: %v", err)
	}
}
func TestPortableNameOrdering(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for _, id := range []string{"host.local", "printer-1", "2", "10", "A", "_meta"} {
		if _, err := s.Insert(ctx, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(ctx, yuruidb.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range page.Records {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "10,2,A,_meta,host.local,printer-1" {
		t.Fatal(ids)
	}
}
