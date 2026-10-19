package cfkv_test

import (
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/drivers/cfkv"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestExplicitUnconditionalWrites(t *testing.T) {
	server := protocoltest.New(t, "cfkv")
	s, err := cfkv.OpenRaw(context.Background(), cfkv.Config{Endpoint: server.URL, AccountID: "account", NamespaceID: "namespace", Collection: "items", Token: auth.StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	id := conformance.ID(t)
	if c := s.Capabilities(); c.ConditionalMutations || c.StrongReads || !c.Distributed {
		t.Fatalf("KV capabilities: %+v", c)
	}
	if _, err = s.Insert(ctx, id, nil); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, id, nil, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, id, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	r, err := s.Set(ctx, id, []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got.Revision != r.Revision || string(got.Value) != "value" {
		t.Fatalf("get: %+v %v", got, err)
	}
	for i := 0; i < 9; i++ {
		if _, err = s.Set(ctx, conformance.ID(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	cursor := ""
	for i := 0; i < 20; i++ {
		p, err := s.List(ctx, yuruidb.ListOptions{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		count += len(p.Records)
		if p.Cursor == "" {
			break
		}
		cursor = p.Cursor
	}
	if count != 10 {
		t.Fatalf("listed %d", count)
	}
	if err = s.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTypedKV(t *testing.T) {
	type data struct {
		Text string `json:"text"`
	}
	ctx := context.Background()
	server := protocoltest.New(t, "cfkv")
	s, err := yuruidb.Open[data](ctx, "cfkv", cfkv.Config{Endpoint: server.URL, AccountID: "account", NamespaceID: "namespace", Collection: "items", Token: auth.StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := conformance.ID(t)
	if _, err := s.Insert(ctx, id, data{}); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, id, data{}, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	r, err := s.Set(ctx, id, data{Text: "JSON"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got != r || got.ID() != id {
		t.Fatalf("typed KV: %+v %v", got, err)
	}
	page, err := s.List(ctx, yuruidb.ListOptions{})
	if err != nil || len(page.Records) != 1 || page.Records[0] != r {
		t.Fatalf("typed KV page: %+v %v", page, err)
	}
	if err := s.Remove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}
