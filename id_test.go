package yuruidb

import (
	"context"
	"errors"
	"testing"
)

func TestUUIDOrderingAndRollback(t *testing.T) {
	previous := ""
	for i := 0; i < 10000; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateID(id); err != nil {
			t.Fatal(err)
		}
		if id <= previous {
			t.Fatalf("%s <= %s", id, previous)
		}
		previous = id
	}
	generator.Lock()
	generator.millis += 1000
	ms := generator.millis
	for i := 5; i >= 0; i-- {
		generator.last[i] = byte(ms)
		ms >>= 8
	}
	generator.Unlock()
	id, err := NewID()
	if err != nil || id <= previous {
		t.Fatalf("clock rollback: %s %v", id, err)
	}
}
func TestInvalidIdentifiers(t *testing.T) {
	for _, id := range []string{"", "01912345-1234-4234-8234-123456789abc", "01912345-1234-7234-c234-123456789abc", "01912345-1234-7234-8234-123456789abC", "../bad"} {
		if !errors.Is(ValidateID(id), ErrInvalid) {
			t.Fatal(id)
		}
	}
}
func TestMissingDriver(t *testing.T) {
	if _, err := Open(context.Background(), "not-imported", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
