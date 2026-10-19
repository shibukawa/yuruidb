package yuruidb

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestNeutralDriverConfig(t *testing.T) {
	type config struct {
		Collection string
		Timeout    time.Duration
		Client     *http.Client
		Provider   func(context.Context) (string, error)
	}
	client := &http.Client{}
	provider := func(context.Context) (string, error) { return "token", nil }
	c, err := DecodeConfig[config](Config{"collection": "items", "Timeout": "2s", "Client": client, "Provider": provider})
	if err != nil || c.Collection != "items" || c.Timeout != 2*time.Second || c.Client != client {
		t.Fatalf("config: %+v %v", c, err)
	}
	token, err := c.Provider(context.Background())
	if err != nil || token != "token" {
		t.Fatal(err)
	}
	for _, input := range []any{Config{"typo": true}, Config{"Timeout": "not-a-duration"}, Config{"Collection": 42}, Config{"Collection": nil}, Config{"Collection": "a", "collection": "b"}, "invalid"} {
		if _, err = DecodeConfig[config](input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input %T: %v", input, err)
		}
	}
}
