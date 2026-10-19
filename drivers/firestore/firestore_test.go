package firestore_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/drivers/firestore"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		server := protocoltest.New(t, "firestore")
		open := func() yuruidb.RawStore {
			s, err := firestore.OpenRaw(context.Background(), firestore.Config{Endpoint: server.URL + "/v1", Project: "project", Collection: "items", Token: auth.StaticToken("test-token")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
