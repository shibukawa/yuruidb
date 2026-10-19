package r2_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/drivers/r2"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.Store, yuruidb.Store) {
		server := protocoltest.New(t, "r2")
		open := func() yuruidb.Store {
			s, err := r2.Open(context.Background(), r2.Config{Endpoint: server.URL, Bucket: "bucket", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret"})})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
