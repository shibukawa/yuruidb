package s3api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shibukawa/yuruidb"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
)

func credentials() awsauth.Provider {
	return awsauth.Static(awsauth.Credentials{AccessKey: "test", SecretKey: "test"})
}

func TestProbeCleanupFailureCannotPoisonCollection(t *testing.T) {
	fixture := protocoltest.New(t, "s3")
	var denyCleanup atomic.Bool
	denyCleanup.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && strings.Contains(r.URL.Path, "probe") && !strings.HasPrefix(r.URL.Path, "/bucket/.yuruidb-probes/") {
			t.Error("probe inside collection")
		}
		if r.Method == "DELETE" {
			if r.Header.Get("If-Match") != "" {
				w.WriteHeader(405)
				return
			}
			if denyCleanup.Load() {
				w.WriteHeader(403)
				return
			}
		}
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	config := Config{Endpoint: server.URL, Bucket: "bucket", Collection: "items", Credentials: credentials()}
	if _, err := Open(context.Background(), config, "s3"); err == nil {
		t.Fatal("missing cleanup failure")
	}
	denyCleanup.Store(false)
	s, err := Open(context.Background(), config, "s3")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Insert(context.Background(), "job", []byte("live")); err != nil {
		t.Fatal(err)
	}
	if page, err := s.List(context.Background(), yuruidb.ListOptions{}); err != nil || len(page.Records) != 1 {
		t.Fatalf("leftover probe broke List: %+v %v", page, err)
	}
}

type probeRoundTrip func(*http.Request) (*http.Response, error)

func (f probeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProbeCleanupSurvivesCancellation(t *testing.T) {
	fixture := protocoltest.New(t, "s3")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	puts, cleanups := 0, 0
	client := &http.Client{Transport: probeRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" {
			puts++
			if puts == 2 {
				cancel()
				return nil, context.Canceled
			}
		}
		if r.Method == "DELETE" {
			cleanups++
			if r.Context().Err() != nil {
				t.Error("cleanup inherited canceled context")
			}
			if _, ok := r.Context().Deadline(); !ok {
				t.Error("cleanup missing timeout")
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	if _, err := Open(ctx, Config{Endpoint: fixture.URL, Bucket: "bucket", Collection: "items", Credentials: credentials(), Client: client}, "s3"); err == nil {
		t.Fatal("expected canceled verification")
	}
	if cleanups != 1 {
		t.Fatalf("cleanup calls=%d", cleanups)
	}
}
func TestRejectEndpointIgnoringConditions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("ETag", `"ignored"`)
		w.WriteHeader(200)
	}))
	defer server.Close()
	_, err := Open(context.Background(), Config{Endpoint: server.URL, Bucket: "bucket", Credentials: credentials()}, "s3")
	if !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatalf("unconditional endpoint accepted: %v", err)
	}
}
func TestConditionalDeleteProbeAndTombstoneFallback(t *testing.T) {
	fixture := protocoltest.New(t, "s3")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.Header.Get("If-Match") != "" {
			w.WriteHeader(405)
			return
		}
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	raw, err := Open(context.Background(), Config{Endpoint: server.URL, Bucket: "bucket", Collection: "items", Credentials: credentials()}, "s3")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	r, err := raw.Insert(context.Background(), "job", []byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.Delete(context.Background(), "job", r.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Get(context.Background(), "job"); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
	tr, err := newTransport(context.Background(), Config{Endpoint: server.URL, Bucket: "bucket", Collection: "items", Credentials: credentials()}, "s3")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := tr.Read(context.Background(), "items/job")
	if err != nil || !strings.Contains(string(stored.Data), `"deleted":true`) {
		t.Fatalf("expected tombstone: %s %v", stored.Data, err)
	}

}
func TestAWSVirtualHostingAndRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "ap-northeast-1")
	tr, err := newTransport(context.Background(), Config{Bucket: "my-bucket", Credentials: credentials()}, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if tr.base != "https://my-bucket.s3.ap-northeast-1.amazonaws.com" {
		t.Fatal(tr.base)
	}
	dotted, err := newTransport(context.Background(), Config{Bucket: "my.bucket", Credentials: credentials()}, "s3")
	if err != nil || !strings.Contains(dotted.base, "amazonaws.com/my.bucket") {
		t.Fatalf("dotted TLS compatibility: %+v %v", dotted, err)
	}
}
