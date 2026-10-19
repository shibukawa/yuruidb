// Package aws signs HTTP requests with AWS Signature Version 4.
package aws

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/shibukawa/yuruidb/auth"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type Credentials struct{ AccessKey, SecretKey, SessionToken string }
type Provider func(context.Context) (Credentials, error)

func Static(c Credentials) Provider {
	return func(context.Context) (Credentials, error) { return c, nil }
}

// Environment reads credentials for each request, including Lambda's session
// token. It does not implement shared profiles, SSO, or AssumeRole.
func Environment(context.Context) (Credentials, error) {
	return Credentials{os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")}, nil
}
func Signer(region, service string, provider Provider) auth.Authorizer {
	if provider == nil {
		provider = Environment
	}
	return func(r *http.Request) error {
		c, err := provider(r.Context())
		if err != nil {
			return err
		}
		return Sign(r, c, region, service, time.Now())
	}
}
func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func mac(k []byte, s string) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func escape(s string, slash bool) string {
	const digits = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", rune(c)) || slash && c == '/' {
			out.WriteByte(c)
		} else {
			out.WriteByte('%')
			out.WriteByte(digits[c>>4])
			out.WriteByte(digits[c&15])
		}
	}
	return out.String()
}

// Sign signs a replayable request. Its body and URL must not change afterward.
func Sign(r *http.Request, c Credentials, region, service string, now time.Time) error {
	if c.AccessKey == "" || c.SecretKey == "" || region == "" || service == "" {
		return fmt.Errorf("yuruidb: AWS credentials, region and service required")
	}
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		if r.GetBody == nil {
			return fmt.Errorf("yuruidb: signing requires replayable body")
		}
		copy, err := r.GetBody()
		if err != nil {
			return err
		}
		body, err = io.ReadAll(copy)
		copy.Close()
		if err != nil {
			return err
		}
	}
	now = now.UTC()
	stamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payload := hash(body)
	r.Header.Set("X-Amz-Date", stamp)
	r.Header.Set("X-Amz-Content-Sha256", payload)
	if c.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", c.SessionToken)
	} else {
		r.Header.Del("X-Amz-Security-Token")
	}
	headers := map[string]string{"host": r.URL.Host}
	if r.Host != "" {
		headers["host"] = r.Host
	}
	for k, v := range r.Header {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "x-amz-") || lower == "content-type" || lower == "if-match" || lower == "if-none-match" || lower == "range" {
			headers[lower] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, k := range names {
		canonicalHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	// URL.Query().Encode sorts keys but uses form encoding; SigV4 requires RFC3986.
	var parts []string
	for k, values := range r.URL.Query() {
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, escape(k, false)+"="+escape(v, false))
		}
	}
	sort.Strings(parts)
	canonical := strings.Join([]string{r.Method, escape(path, true), strings.Join(parts, "&"), canonicalHeaders.String(), signed, payload}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hash([]byte(canonical))
	key := mac(mac(mac(mac([]byte("AWS4"+c.SecretKey), date), region), service), "aws4_request")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+hex.EncodeToString(mac(key, toSign)))
	return nil
}
