package blob_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/blob"
	"github.com/shibukawa/yuruidb/drivers/bbolt"
	"github.com/shibukawa/yuruidb/drivers/gcs"
	"github.com/shibukawa/yuruidb/drivers/r2"
	"github.com/shibukawa/yuruidb/drivers/s3"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
)

func TestBlobBackends(t *testing.T) {
	for _, kind := range []string{"bbolt", "s3", "r2", "gcs"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var open func(string) (*blob.Store, error)
			if kind == "bbolt" {
				path := filepath.Join(t.TempDir(), "db")
				open = func(collection string) (*blob.Store, error) {
					return bbolt.OpenBlobs(ctx, bbolt.Config{Path: path, Collection: collection})
				}
			} else {
				server := protocoltest.New(t, kind)
				open = func(collection string) (*blob.Store, error) {
					if kind == "gcs" {
						return gcs.OpenBlobs(ctx, gcs.Config{Endpoint: server.URL, Bucket: "bucket", Collection: collection, Token: auth.StaticToken("test-token")})
					}
					config := s3.Config{Endpoint: server.URL, Bucket: "bucket", Collection: collection, Credentials: awsauth.Static(awsauth.Credentials{AccessKey: "key", SecretKey: "secret"})}
					if kind == "r2" {
						return r2.OpenBlobs(ctx, config)
					}
					return s3.OpenBlobs(ctx, config)
				}
			}
			conformance.RunBlobs(t, open)
		})
	}
}

func TestBlobUnconditionalRemoveWithoutConditionalDelete(t *testing.T) {
	fixture := protocoltest.New(t, "s3")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.Header.Get("If-Match") != "" {
			w.WriteHeader(405)
			return
		}
		fixture.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	s, err := s3.OpenBlobs(context.Background(), s3.Config{Endpoint: server.URL, Bucket: "bucket", Collection: "documents", Credentials: awsauth.Static(awsauth.Credentials{AccessKey: "key", SecretKey: "secret"})})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	meta, err := s.Insert(context.Background(), "a", strings.NewReader("document"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), meta.ID, meta.Revision); !errors.Is(err, yuruidb.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := s.Remove(context.Background(), meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), meta.ID); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}
