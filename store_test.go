package yuruidb_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/bbolt"
	"github.com/shibukawa/yuruidb/drivers/memory"
)

type note struct {
	Text   string            `json:"text"`
	Tags   []string          `json:"tags,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

func nextID(t *testing.T) string {
	t.Helper()
	id, err := yuruidb.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTypedBboltPersistence(t *testing.T) {
	ctx := context.Background()
	config := yuruidb.Config{"Path": filepath.Join(t.TempDir(), "records.db"), "Collection": "notes"}
	s, err := yuruidb.Open[note](ctx, "bbolt", config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, laterID := nextID(t), nextID(t)
	if _, err := s.Insert(ctx, laterID, note{Text: "later"}); err != nil {
		t.Fatal(err)
	}
	input := note{Text: "hello", Tags: []string{"go"}, Labels: map[string]string{"lang": "ja"}}
	r, err := s.Insert(ctx, id, input)
	if err != nil || r.ID() != id || r.Revision() == "" || !reflect.DeepEqual(r.Data, input) {
		t.Fatalf("insert: %+v %v", r, err)
	}
	input.Tags[0], input.Labels["lang"] = "changed", "en"
	if r.Data.Tags[0] != "go" || r.Data.Labels["lang"] != "ja" {
		t.Fatal("returned data aliases caller input")
	}
	r.Data.Tags[0] = "changed result"
	got, err := s.Get(ctx, id)
	if err != nil || got.Data.Tags[0] != "go" {
		t.Fatalf("get: %+v %v", got, err)
	}
	updated, err := s.Update(ctx, got.ID(), note{Text: "updated"}, got.Revision())
	if err != nil || updated.Revision() == got.Revision() || updated.ID() != id {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if _, err := s.Update(ctx, id, note{}, got.Revision()); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The direct driver constructor has the same typed API.
	s, err = bbolt.Open[note](ctx, bbolt.Config{Path: config["Path"].(string), Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.List(ctx, yuruidb.ListOptions{Limit: 1})
	if err != nil || len(p.Records) != 1 || p.Records[0].ID() != id || p.Records[0].Data.Text != "updated" || p.Cursor == "" {
		t.Fatalf("first page: %+v %v", p, err)
	}
	p, err = s.List(ctx, yuruidb.ListOptions{Limit: 1, Cursor: p.Cursor})
	if err != nil || len(p.Records) != 1 || p.Records[0].ID() != laterID {
		t.Fatalf("second page: %+v %v", p, err)
	}
	if err := s.Delete(ctx, id, updated.Revision()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if _, err := s.Set(ctx, id, note{}); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatalf("set on CAS backend: %v", err)
	}
	if err := s.Remove(ctx, id); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatalf("remove on CAS backend: %v", err)
	}
}

// This marshaler writes valid JSON that its Go type cannot unmarshal.
type writeOnly struct{}

func (writeOnly) MarshalJSON() ([]byte, error) { return []byte(`"text"`), nil }

type jsonText string

func (v jsonText) MarshalJSON() ([]byte, error) { return json.Marshal(strings.ToUpper(string(v))) }
func (v *jsonText) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	*v = jsonText(strings.ToLower(text))
	return nil
}

func TestJSONFailuresDoNotMutate(t *testing.T) {
	ctx := context.Background()
	s, err := memory.Open[any](ctx, memory.Config{Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	for _, data := range []any{func() {}, make(chan int), math.NaN(), cycle, json.RawMessage(`{`)} {
		id := nextID(t)
		if _, err := s.Insert(ctx, id, data); !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatalf("insert %T: %v", data, err)
		}
		if _, err := s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
			t.Fatalf("failed insert wrote data: %v", err)
		}
	}
	id := nextID(t)
	r, err := s.Insert(ctx, id, "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, id, math.Inf(1), r.Revision()); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got.Revision() != r.Revision() || got.Data != "original" {
		t.Fatalf("failed update changed data: %+v %v", got, err)
	}
	raw, err := memory.OpenRaw(ctx, memory.Config{Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	writeStore := yuruidb.Wrap[writeOnly](raw)
	id = nextID(t)
	if _, err := writeStore.Insert(ctx, id, writeOnly{}); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatalf("incompatible custom marshaler: %v", err)
	}
	if _, err := raw.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatalf("decode validation happened after write: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Insert(canceled, nextID(t), func() {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled insert: %v", err)
	}
}

func checkRoundTrip[T any](t *testing.T, input T) {
	t.Helper()
	ctx := context.Background()
	raw, err := memory.OpenRaw(ctx, memory.Config{Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	s := yuruidb.Wrap[T](raw)
	r, err := s.Insert(ctx, nextID(t), input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, r.ID())
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("round trip: %+v vs %+v; %v", got, r, err)
	}
	wire, err := raw.Get(ctx, r.ID())
	expected, encodeErr := json.Marshal(input)
	if err != nil || encodeErr != nil || string(wire.Value) != string(expected) {
		t.Fatalf("JSON payload: %s vs %s; %v %v", wire.Value, expected, err, encodeErr)
	}
}

func TestJSONTypes(t *testing.T) {
	checkRoundTrip(t, &note{Text: "pointer"})
	checkRoundTrip[*note](t, nil)
	checkRoundTrip(t, map[string][]int{"values": {1, 2}})
	checkRoundTrip(t, json.RawMessage(`{"arbitrary":true}`))
	checkRoundTrip(t, jsonText("custom"))
}

func TestStoredJSONDecodeErrors(t *testing.T) {
	ctx := context.Background()
	for _, value := range []string{`not JSON`, `{"text":42}`} {
		t.Run(value, func(t *testing.T) {
			raw, err := memory.OpenRaw(ctx, memory.Config{Collection: "notes"})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			s := yuruidb.Wrap[note](raw)
			if _, err := s.Insert(ctx, nextID(t), note{Text: "valid"}); err != nil {
				t.Fatal(err)
			}
			id := nextID(t)
			if _, err := raw.Insert(ctx, id, []byte(value)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, id); err == nil || !strings.Contains(err.Error(), id) {
				t.Fatalf("decode error omitted identity: %v", err)
			}
			page, err := s.List(ctx, yuruidb.ListOptions{})
			if err == nil || len(page.Records) != 0 || page.Cursor != "" {
				t.Fatalf("partial page after decode failure: %+v %v", page, err)
			}
		})
	}
}
