// Package gcs registers Google Cloud Storage via its JSON HTTP API.
package gcs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
)

type Config struct {
	Endpoint, Bucket, Collection string
	Client                       *http.Client
	Token                        auth.TokenProvider
	Authorize                    auth.Authorizer
}
type transport struct {
	client                   httpx.Client
	endpoint, bucket, prefix string
}

func init() {
	yuruidb.Register("gcs", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return OpenRaw(ctx, c)
	})
}
func OpenRaw(ctx context.Context, c Config) (yuruidb.RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Bucket == "" {
		return nil, fmt.Errorf("%w: GCS bucket required", yuruidb.ErrInvalid)
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://storage.googleapis.com"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = auth.Bearer(c.Token)
	}
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint, c.Bucket, c.Collection + "/"}
	return recordstore.New(t, c.Collection, "gcs:"+endpoint+"/"+c.Bucket, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}
func (t *transport) base() string {
	return t.endpoint + "/storage/v1/b/" + url.PathEscape(t.bucket) + "/o"
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	r, err := t.client.Do(ctx, "GET", t.base()+"/"+url.PathEscape(key)+"?alt=media", nil, nil, false)
	if err != nil {
		return recordstore.Item{}, err
	}
	token := r.Header.Get("X-Goog-Generation")
	if token == "" {
		return recordstore.Item{}, fmt.Errorf("yuruidb: GCS media response lacks generation")
	}
	return recordstore.Item{Data: r.Body, Token: token}, nil
}
func (t *transport) Write(ctx context.Context, key string, b []byte, expected string) error {
	if expected == "" {
		expected = "0"
	}
	q := url.Values{"uploadType": {"media"}, "name": {key}, "ifGenerationMatch": {expected}}
	h := http.Header{"Content-Type": {"application/json"}}
	_, err := t.client.Do(ctx, "POST", t.endpoint+"/upload/storage/v1/b/"+url.PathEscape(t.bucket)+"/o?"+q.Encode(), b, h, true)
	return err
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	if n > 999 {
		n = 999
	}
	q := url.Values{"prefix": {t.prefix}, "maxResults": {fmt.Sprint(n + 1)}}
	if after != "" {
		q.Set("startOffset", after)
	}
	keys := []string{}
	seen := map[string]bool{}
	for {
		r, err := t.client.Do(ctx, "GET", t.base()+"?"+q.Encode(), nil, nil, false)
		if err != nil {
			return nil, false, err
		}
		var out struct {
			Next  string `json:"nextPageToken"`
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		if err = json.Unmarshal(r.Body, &out); err != nil {
			return nil, false, err
		}
		for _, item := range out.Items {
			if item.Name > after {
				keys = append(keys, item.Name)
			}
		}
		if len(keys) >= n {
			more := len(keys) > n || out.Next != ""
			if len(keys) > n {
				keys = keys[:n]
			}
			return keys, more, nil
		}
		if out.Next == "" {
			return keys, false, nil
		}
		if seen[out.Next] {
			return nil, false, fmt.Errorf("yuruidb: repeated GCS page token")
		}
		seen[out.Next] = true
		q.Set("pageToken", out.Next)
	}
}
func (t *transport) Close() error { return nil }

// Open opens a JSON-typed collection using this driver.
func Open[T any](ctx context.Context, c Config) (*yuruidb.Store[T], error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap[T](raw), nil
}
