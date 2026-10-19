package bbolt_test

import (
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/bbolt"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"path/filepath"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		s, err := bbolt.OpenRaw(context.Background(), bbolt.Config{Path: filepath.Join(t.TempDir(), "db"), Collection: "items"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, s
	})
}
func TestPersistenceAndCollections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	config := bbolt.Config{Path: path, Collection: "items"}
	s, err := bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	id := conformance.ID(t)
	r, err := s.Insert(ctx, id, []byte("saved"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got.Revision != r.Revision || string(got.Value) != "saved" {
		t.Fatalf("reopen: %v %v", got, err)
	}
	if err = s.Delete(ctx, id, got.Revision); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatalf("deleted record returned after reopen: %v", err)
	}
	s.Close()
	config.Collection = "other"
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
	if p, err := s.List(ctx, yuruidb.ListOptions{}); err != nil || len(p.Records) != 0 {
		t.Fatalf("collection: %v %v", p, err)
	}
}
