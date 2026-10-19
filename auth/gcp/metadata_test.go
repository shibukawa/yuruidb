package gcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMetadataCache(t *testing.T) {
	var calls atomic.Int32
	p := Metadata(&http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Metadata-Flavor") != "Google" {
			t.Error("metadata header missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"token","expires_in":3600}`))}, nil
	})})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := p(context.Background())
			if err != nil || token != "token" {
				t.Errorf("token: %s %v", token, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("metadata calls: %d", calls.Load())
	}
}
