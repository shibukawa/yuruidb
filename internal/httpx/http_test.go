package httpx

import (
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUnknownOutcomeAndRedaction(t *testing.T) {
	c := Client{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("SECRET URL transport details") })}, Auth: auth.QueryToken("auth", auth.StaticToken("PRIVATE_TOKEN"))}
	_, err := c.Do(context.Background(), "PUT", "https://example.invalid/item", nil, nil, true)
	if !errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "PRIVATE_TOKEN") {
		t.Fatal("secret in error")
	}
	_, err = c.Do(context.Background(), "GET", "https://example.invalid/item", nil, nil, false)
	if errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		t.Fatal("read marked as mutation")
	}
}
func TestStatusAndMalformedMutation(t *testing.T) {
	for _, code := range []int{400, 401, 404, 409, 412, 429, 500, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c := Client{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("secret server response")), Header: make(http.Header)}, nil
			})}}
			_, err := c.Do(context.Background(), "PUT", "https://example.invalid/item", nil, nil, true)
			if err == nil {
				t.Fatal("missing error")
			}
			if errors.Is(err, yuruidb.ErrOutcomeUnknown) != (code >= 500) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("remote content in error")
			}
		})
	}
	c := Client{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("broken-json")), Header: make(http.Header)}, nil
	})}}
	var out any
	if err := c.JSON(context.Background(), "POST", "https://example.invalid/item", nil, &out, nil, true); !errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
}
