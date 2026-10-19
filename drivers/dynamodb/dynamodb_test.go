package dynamodb_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/drivers/dynamodb"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.Store, yuruidb.Store) {
		server := protocoltest.New(t, "dynamodb")
		open := func() yuruidb.Store {
			s, err := dynamodb.Open(context.Background(), dynamodb.Config{Endpoint: server.URL, Region: "test-region", Table: "table", Collection: "items", Credentials: aws.Static(aws.Credentials{AccessKey: "key", SecretKey: "secret"})})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
