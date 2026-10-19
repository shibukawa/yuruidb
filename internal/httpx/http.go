package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxResponse = 32 << 20

type Client struct {
	HTTP *http.Client
	Auth auth.Authorizer
}
type Response struct {
	Body   []byte
	Header http.Header
}
type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return fmt.Sprintf("yuruidb: remote HTTP %d (%s)", e.Status, e.Code) }
func Endpoint(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: endpoint must be an HTTP(S) URL without credentials/query/fragment", yuruidb.ErrInvalid)
	}
	return strings.TrimRight(value, "/"), nil
}
func (c Client) Do(ctx context.Context, method, target string, body []byte, headers http.Header, mutation bool) (Response, error) {
	var result Response
	if err := ctx.Err(); err != nil {
		return result, err
	}
	r, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("%w: malformed request", yuruidb.ErrInvalid)
	}
	if headers != nil {
		r.Header = headers.Clone()
	}
	if c.Auth != nil {
		if err = c.Auth(r); err != nil {
			return result, err
		}
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(r)
	if err != nil {
		safe := fmt.Errorf("yuruidb: remote transport failed")
		if ctx.Err() != nil {
			safe = fmt.Errorf("yuruidb: remote request: %w", ctx.Err())
		}
		if mutation {
			return result, errors.Join(yuruidb.ErrOutcomeUnknown, safe)
		}
		return result, safe
	}
	defer response.Body.Close()
	result.Header = response.Header.Clone()
	result.Body, err = io.ReadAll(io.LimitReader(response.Body, MaxResponse+1))
	if err != nil || len(result.Body) > MaxResponse {
		safe := fmt.Errorf("yuruidb: unreadable or oversized remote response")
		if mutation {
			return Response{}, errors.Join(yuruidb.ErrOutcomeUnknown, safe)
		}
		return Response{}, safe
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return result, nil
	}
	remote := &Error{Status: response.StatusCode, Code: http.StatusText(response.StatusCode)}
	switch response.StatusCode {
	case 404:
		return result, errors.Join(yuruidb.ErrNotFound, remote)
	case 409, 412:
		return result, errors.Join(yuruidb.ErrConflict, remote)
	}
	if mutation && response.StatusCode >= 500 {
		return result, errors.Join(yuruidb.ErrOutcomeUnknown, remote)
	}
	return result, remote
}
func (c Client) JSON(ctx context.Context, method, target string, input, output any, headers http.Header, mutation bool) error {
	var b []byte
	var err error
	if input != nil {
		b, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	if headers == nil {
		headers = make(http.Header)
	} else {
		headers = headers.Clone()
	}
	headers.Set("Content-Type", "application/json")
	result, err := c.Do(ctx, method, target, b, headers, mutation)
	if err != nil {
		return err
	}
	if output != nil {
		if err = json.Unmarshal(result.Body, output); err != nil {
			if mutation {
				return errors.Join(yuruidb.ErrOutcomeUnknown, fmt.Errorf("yuruidb: malformed mutation response"))
			}
			return fmt.Errorf("yuruidb: malformed remote JSON: %w", err)
		}
	}
	return nil
}
