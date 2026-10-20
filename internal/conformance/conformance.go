// Package conformance contains the shared acceptance suite used by driver tests.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"sync"
	"sync/atomic"
	"testing"
)

type Open func(*testing.T) (yuruidb.RawStore, yuruidb.RawStore)

func ID(t *testing.T) string {
	t.Helper()
	id, err := yuruidb.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func Run(t *testing.T, open Open) {
	t.Run("TypedJSON", func(t *testing.T) {
		type data struct {
			Text  string `json:"text"`
			Count int    `json:"count"`
		}
		rawA, rawB := open(t)
		a, b := yuruidb.Wrap(rawA), yuruidb.Wrap(rawB)
		if err := a.Bind[data]("items"); err != nil {
			t.Fatal(err)
		}
		if err := b.Bind[data]("items"); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		id := ID(t)
		r, err := a.Insert(ctx, id, data{Text: "JSON 日本語", Count: 1})
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.Get[data](ctx, id)
		if err != nil || got != r || got.ID() != id {
			t.Fatalf("typed get: %+v vs %+v; %v", got, r, err)
		}
		updated, err := b.Update(ctx, id, data{Text: "updated", Count: 2}, r.Revision())
		if err != nil || updated.Revision() == r.Revision() {
			t.Fatalf("typed update: %+v %v", updated, err)
		}
		if _, err := a.Update(ctx, id, data{}, r.Revision()); !errors.Is(err, yuruidb.ErrConflict) {
			t.Fatalf("typed stale revision: %v", err)
		}
		page, err := a.List[data](ctx, yuruidb.ListOptions{Limit: 1})
		if err != nil || len(page.Records) != 1 || page.Records[0] != updated {
			t.Fatalf("typed list: %+v %v", page, err)
		}
		if err := a.Delete[data](ctx, id, updated.Revision()); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Get[data](ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
			t.Fatalf("typed deleted: %v", err)
		}
	})
	t.Run("CRUDAndABA", func(t *testing.T) {
		a, b := open(t)
		ctx := context.Background()
		id := ID(t)
		payload := []byte{0, 255, 1, 42}
		r, err := a.Insert(ctx, id, payload)
		if err != nil {
			t.Fatal(err)
		}
		payload[0] = 88
		got, err := b.Get(ctx, id)
		if err != nil || got.Revision != r.Revision || !bytes.Equal(got.Value, []byte{0, 255, 1, 42}) {
			t.Fatalf("read: %+v %v", got, err)
		}
		got.Value[0] = 99
		if _, err = a.Insert(ctx, id, []byte("duplicate")); !errors.Is(err, yuruidb.ErrAlreadyExists) {
			t.Fatalf("duplicate: %v", err)
		}
		next, err := b.Update(ctx, id, []byte("next"), r.Revision)
		if err != nil || next.Revision == r.Revision || next.ID != id {
			t.Fatalf("update: %+v %v", next, err)
		}
		if _, err = a.Update(ctx, id, []byte("lost"), r.Revision); !errors.Is(err, yuruidb.ErrConflict) {
			t.Fatalf("stale update: %v", err)
		}
		if err = a.Delete(ctx, id, r.Revision); !errors.Is(err, yuruidb.ErrConflict) {
			t.Fatalf("stale delete: %v", err)
		}
		if err = a.Delete(ctx, id, next.Revision); err != nil {
			t.Fatal(err)
		}
		if _, err = b.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
			t.Fatalf("deleted get: %v", err)
		}
		recreated, err := b.Insert(ctx, id, []byte("recreated"))
		if err != nil {
			t.Fatal(err)
		}
		if recreated.Revision == next.Revision {
			t.Fatal("revision reused")
		}
		if _, err = a.Update(ctx, id, nil, next.Revision); !errors.Is(err, yuruidb.ErrConflict) {
			t.Fatalf("ABA update: %v", err)
		}
		if err = a.Delete(ctx, id, next.Revision); !errors.Is(err, yuruidb.ErrConflict) {
			t.Fatalf("ABA delete: %v", err)
		}
	})
	t.Run("CreationOrderPagination", func(t *testing.T) {
		a, b := open(t)
		ctx := context.Background()
		ids := make([]string, 23)
		for i := range ids {
			ids[i] = ID(t)
		}
		for i := len(ids) - 1; i >= 0; i-- {
			if _, err := a.Insert(ctx, ids[i], []byte(fmt.Sprint(i))); err != nil {
				t.Fatal(err)
			}
		}
		r, err := a.Get(ctx, ids[2])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Update(ctx, r.ID, []byte("modified"), r.Revision); err != nil {
			t.Fatal(err)
		}
		deleted := map[string]bool{}
		for _, i := range []int{0, 4, 5, 6, 7, 8, 9, 21, 22} {
			r, err := b.Get(ctx, ids[i])
			if err != nil {
				t.Fatal(err)
			}
			if err = b.Delete(ctx, r.ID, r.Revision); err != nil {
				t.Fatal(err)
			}
			deleted[r.ID] = true
		}
		seen := []string{}
		cursor := ""
		for count := 0; ; count++ {
			if count > 30 {
				t.Fatal("cursor did not terminate")
			}
			p, err := b.List(ctx, yuruidb.ListOptions{Limit: 3, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Records) > 3 {
				t.Fatal("limit exceeded")
			}
			for _, r := range p.Records {
				seen = append(seen, r.ID)
			}
			if p.Cursor == "" {
				break
			}
			if p.Cursor == cursor {
				t.Fatal("cursor did not progress")
			}
			cursor = p.Cursor
		}
		expected := []string{}
		for _, id := range ids {
			if !deleted[id] {
				expected = append(expected, id)
			}
		}
		if fmt.Sprint(seen) != fmt.Sprint(expected) {
			t.Fatalf("ids:\n%v\nwant:\n%v", seen, expected)
		}
		if _, err = a.List(ctx, yuruidb.ListOptions{Cursor: "invalid"}); !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatalf("invalid cursor: %v", err)
		}
	})
	t.Run("ConcurrentClients", func(t *testing.T) {
		a, b := open(t)
		ctx := context.Background()
		id := ID(t)
		r, err := a.Insert(ctx, id, []byte("initial"))
		if err != nil {
			t.Fatal(err)
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				s := a
				if i%2 == 1 {
					s = b
				}
				_, err := s.Update(ctx, id, []byte(fmt.Sprint(i)), r.Revision)
				if err == nil {
					wins.Add(1)
				} else if !errors.Is(err, yuruidb.ErrConflict) {
					t.Errorf("race error: %v", err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("successful conditional writes: %d", wins.Load())
		}
		id = ID(t)
		wins.Store(0)
		start = make(chan struct{})
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				s := a
				if i%2 == 1 {
					s = b
				}
				_, err := s.Insert(ctx, id, nil)
				if err == nil {
					wins.Add(1)
				} else if !errors.Is(err, yuruidb.ErrAlreadyExists) {
					t.Errorf("insert race: %v", err)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("successful inserts: %d", wins.Load())
		}
	})

	t.Run("PortableNames", func(t *testing.T) {
		a, b := open(t)
		ctx := context.Background()
		for _, id := range []string{"host.local", "printer-1", "2", "10", "A", "_meta"} {
			if _, err := a.Insert(ctx, id, []byte(id)); err != nil {
				t.Fatal(err)
			}
		}
		page, err := b.List(ctx, yuruidb.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range page.Records {
			ids = append(ids, r.ID)
		}
		if fmt.Sprint(ids) != "[10 2 A _meta host.local printer-1]" {
			t.Fatal(ids)
		}
	})
	t.Run("ValidationCancellation", func(t *testing.T) {
		a, _ := open(t)
		ctx := context.Background()
		if _, err := a.Insert(ctx, "bad/id", nil); !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatal(err)
		}
		id := ID(t)
		if _, err := a.Update(ctx, id, nil, ""); !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatal(err)
		}
		if err := a.Delete(ctx, id, ""); !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatal(err)
		}
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := a.Get(cancelCtx, id); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
		for _, limit := range []int{-1, 1001} {
			if _, err := a.List(ctx, yuruidb.ListOptions{Limit: limit}); !errors.Is(err, yuruidb.ErrInvalid) {
				t.Fatalf("limit: %v", err)
			}
		}
	})
	t.Run("Close", func(t *testing.T) {
		a, _ := open(t)
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Get(context.Background(), ID(t)); !errors.Is(err, yuruidb.ErrClosed) {
			t.Fatalf("closed Get: %v", err)
		}
		if _, err := a.List(context.Background(), yuruidb.ListOptions{}); !errors.Is(err, yuruidb.ErrClosed) {
			t.Fatalf("closed List: %v", err)
		}
	})

}
