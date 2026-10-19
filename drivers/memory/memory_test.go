package memory_test

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/memory"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		s, err := memory.OpenRaw(context.Background(), memory.Config{Collection: "items"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, s
	})
}
