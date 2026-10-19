// Package gcp provides a small cached token provider for GCP metadata servers.
// For local ADC, service-account JWTs or workload federation, inject a token
// provider from the caller's chosen authentication library instead.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shibukawa/yuruidb/auth"
	"io"
	"net/http"
	"sync"
	"time"
)

func Metadata(client *http.Client) auth.TokenProvider {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	var mu sync.Mutex
	var token string
	var expiry time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Add(time.Minute).Before(expiry) {
			return token, nil
		}
		r, err := http.NewRequestWithContext(ctx, "GET", "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
		if err != nil {
			return "", err
		}
		r.Header.Set("Metadata-Flavor", "Google")
		response, err := client.Do(r)
		if err != nil {
			return "", fmt.Errorf("yuruidb: GCP metadata request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return "", fmt.Errorf("yuruidb: GCP metadata HTTP %d", response.StatusCode)
		}
		var data struct {
			Token   string `json:"access_token"`
			Expires int    `json:"expires_in"`
		}
		if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&data); err != nil {
			return "", err
		}
		if data.Token == "" || data.Expires <= 0 {
			return "", fmt.Errorf("yuruidb: invalid metadata token response")
		}
		token = data.Token
		expiry = time.Now().Add(time.Duration(data.Expires) * time.Second)
		return token, nil
	}
}
