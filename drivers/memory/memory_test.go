package memory_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/memory"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.Store, yuruidb.Store) {
		s, err := memory.Open(context.Background(), memory.Config{Collection: "items"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, s
	})
}
