package cfkv_test

import (
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/drivers/cfkv"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"io"
	"net/http"
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
	s, err := yuruidb.Open(ctx, "cfkv", cfkv.Config{Endpoint: server.URL, AccountID: "account", NamespaceID: "namespace", Collection: "items", Token: auth.StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Bind[data]("items"); err != nil {
		t.Fatal(err)
	}
	id := conformance.ID(t)
	if _, err := s.Insert(ctx, id, data{}); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, id, data{}, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := s.Delete[data](ctx, id, "revision"); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	r, err := s.Set(ctx, id, data{Text: "JSON"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get[data](ctx, id)
	if err != nil || got != r || got.ID() != id {
		t.Fatalf("typed KV: %+v %v", got, err)
	}
	page, err := s.List[data](ctx, yuruidb.ListOptions{})
	if err != nil || len(page.Records) != 1 || page.Records[0] != r {
		t.Fatalf("typed KV page: %+v %v", page, err)
	}
	if err := s.Remove[data](ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get[data](ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}

type lostResponse func(*http.Request) (*http.Response, error)

func (f lostResponse) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSetOutcomeRetainsAttemptedRevision(t *testing.T) {
	server := protocoltest.New(t, "cfkv")
	client := &http.Client{Transport: lostResponse(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if err == nil && r.Method == "PUT" {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return nil, errors.New("response lost")
		}
		return response, err
	})}
	raw, err := cfkv.OpenRaw(context.Background(), cfkv.Config{Endpoint: server.URL, AccountID: "account", NamespaceID: "namespace", Collection: "items", Client: client, Token: auth.StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, err = raw.Set(context.Background(), "job", []byte("written"))
	var attempt *yuruidb.MutationError
	if !errors.Is(err, yuruidb.ErrOutcomeUnknown) || !errors.As(err, &attempt) || attempt.Operation != "set" {
		t.Fatalf("missing attempted revision: %v", err)
	}
	current, err := raw.Get(context.Background(), "job")
	if err != nil || !attempt.MatchesRevision(current.Revision) {
		t.Fatalf("cannot reconcile KV write: %+v %v", current, err)
	}
}
