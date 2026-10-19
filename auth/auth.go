// Package auth provides injectable request authorization without cloud SDKs.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type Authorizer func(*http.Request) error
type TokenProvider func(context.Context) (string, error)

func StaticToken(token string) TokenProvider {
	return func(context.Context) (string, error) {
		if token == "" {
			return "", fmt.Errorf("yuruidb: empty token")
		}
		return token, nil
	}
}
func Bearer(provider TokenProvider) Authorizer {
	return func(r *http.Request) error {
		if provider == nil {
			return fmt.Errorf("yuruidb: token provider required")
		}
		token, err := provider(r.Context())
		if err != nil {
			return err
		}
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return fmt.Errorf("yuruidb: invalid token")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// QueryToken supports APIs, such as Firebase ID-token authentication, that
// explicitly require a token in a query parameter. Errors never include URLs.
func QueryToken(name string, provider TokenProvider) Authorizer {
	return func(r *http.Request) error {
		if provider == nil {
			return fmt.Errorf("yuruidb: token provider required")
		}
		token, err := provider(r.Context())
		if err != nil {
			return err
		}
		q := r.URL.Query()
		q.Set(name, token)
		r.URL.RawQuery = q.Encode()
		return nil
	}
}
