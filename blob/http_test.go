package blob_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/drivers/gcs"
	"github.com/shibukawa/yuruidb/drivers/s3"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
)

type repeated byte

func (r repeated) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}
func TestStreamingDocumentExceedsJSONResponseLimit(t *testing.T) {
	const size = int64(34 << 20)
	var stored bool
	expectedHash := sha256.New()
	io.Copy(expectedHash, io.LimitReader(repeated('x'), size))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing signature")
		}
		switch r.Method {
		case "PUT":
			if stored || r.Header.Get("If-None-Match") != "*" {
				w.WriteHeader(412)
				return
			}
			hash := sha256.New()
			n, err := io.Copy(hash, r.Body)
			if err != nil || n != size || string(hash.Sum(nil)) != string(expectedHash.Sum(nil)) {
				t.Errorf("upload n=%d err=%v", n, err)
			}
			stored = true
			w.Header().Set("ETag", `"blob-v1"`)
		case "GET":
			if !stored {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("ETag", `"blob-v1"`)
			io.Copy(w, io.LimitReader(repeated('x'), size))
		case "DELETE":
			if r.Header.Get("If-Match") != `"blob-v1"` {
				w.WriteHeader(412)
				return
			}
			stored = false
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	// Explicit AWS endpoint with a custom transport avoids compatibility probes
	// against this narrowly scoped streaming fixture.
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		u := *r.URL
		target, _ := http.NewRequest("GET", server.URL, nil)
		u.Scheme = target.URL.Scheme
		u.Host = target.URL.Host
		clone.URL = &u
		return http.DefaultTransport.RoundTrip(clone)
	})
	store, err := s3.OpenBlobs(context.Background(), s3.Config{Bucket: "bucket", Collection: "documents", Client: &http.Client{Transport: transport}, Credentials: awsauth.Static(awsauth.Credentials{AccessKey: "test", SecretKey: "test"})})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.Insert(context.Background(), "document-1", io.LimitReader(repeated('x'), size))
	if err != nil || meta.Size != size {
		t.Fatalf("insert: %+v %v", meta, err)
	}
	if _, err = store.Insert(context.Background(), "document-1", strings.NewReader("duplicate")); !errors.Is(err, yuruidb.ErrAlreadyExists) {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), "document-1")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, reader)
	reader.Close()
	if err != nil || n != size || string(hash.Sum(nil)) != string(expectedHash.Sum(nil)) {
		t.Fatalf("download n=%d err=%v", n, err)
	}
	if err = store.Delete(context.Background(), meta.ID, `"wrong"`); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatal(err)
	}
	if err = store.Delete(context.Background(), meta.ID, meta.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Open(context.Background(), meta.ID); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGCSStreamingGeneration(t *testing.T) {
	server := protocoltest.New(t, "gcs")
	store, err := gcs.OpenBlobs(context.Background(), gcs.Config{Endpoint: server.URL, Bucket: "bucket", Collection: "documents", Token: auth.StaticToken("test-token")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.Insert(context.Background(), "document", strings.NewReader("large document"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(b) != "large document" || reader.Revision != meta.Revision {
		t.Fatalf("read: %s %v", b, err)
	}
	if err = store.Delete(context.Background(), meta.ID, "wrong"); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatal(err)
	}
	if err = store.Delete(context.Background(), meta.ID, meta.Revision); err != nil {
		t.Fatal(err)
	}
}
