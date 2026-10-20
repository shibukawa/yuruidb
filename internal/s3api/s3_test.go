package s3api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/yuruidb"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
)

func credentials() awsauth.Provider {
	return awsauth.Static(awsauth.Credentials{AccessKey: "test", SecretKey: "test"})
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
