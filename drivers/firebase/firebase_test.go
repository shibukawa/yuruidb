package firebase_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/drivers/firebase"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"github.com/shibukawa/yuruidb/internal/protocoltest"
	"testing"
)

func TestHTTPConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.Store, yuruidb.Store) {
		server := protocoltest.New(t, "firebase")
		open := func() yuruidb.Store {
			s, err := firebase.Open(context.Background(), firebase.Config{Endpoint: server.URL, Collection: "items", Token: auth.StaticToken("test-token")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
