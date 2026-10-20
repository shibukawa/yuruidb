// Package gcs registers Google Cloud Storage via its JSON HTTP API.
package gcs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/blob"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
	_, err := t.WriteItem(ctx, key, b, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, b []byte, expected string) (string, error) {
	if len(b) > httpx.MaxResponse {
		return "", fmt.Errorf("%w: encoded record exceeds 32 MiB; use OpenBlobs", yuruidb.ErrInvalid)
	}

	if expected == "" {
		expected = "0"
	}
	q := url.Values{"uploadType": {"media"}, "name": {key}, "ifGenerationMatch": {expected}}
	h := http.Header{"Content-Type": {"application/json"}}
	response, err := t.client.Do(ctx, "POST", t.endpoint+"/upload/storage/v1/b/"+url.PathEscape(t.bucket)+"/o?"+q.Encode(), b, h, true)
	if err != nil {
		return "", err
	}
	var out struct {
		Generation string `json:"generation"`
	}
	if json.Unmarshal(response.Body, &out) != nil || out.Generation == "" {
		return "", fmt.Errorf("%w: missing GCS generation", yuruidb.ErrOutcomeUnknown)
	}
	return out.Generation, nil
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

// Open opens a connection. Bind data types to collection names before use.
func Open(ctx context.Context, c Config) (*yuruidb.Store, error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap(raw), nil
}

func (t *transport) ForCollection(ctx context.Context, name string) (recordstore.Transport, error) {
	copy := *t
	copy.prefix = name + "/"
	return &copy, nil
}

func (t *transport) Delete(ctx context.Context, key, expected string) error {
	_, err := t.client.Do(ctx, "DELETE", t.base()+"/"+url.PathEscape(key)+"?"+url.Values{"ifGenerationMatch": {expected}}.Encode(), nil, nil, true)
	return err
}

// OpenBlobs opens a separate collection for immutable streaming documents.
func OpenBlobs(ctx context.Context, c Config) (*blob.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := yuruidb.ValidateCollection(c.Collection); err != nil {
		return nil, err
	}
	if c.Bucket == "" {
		return nil, yuruidb.ErrInvalid
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
	location := func(id string) string {
		return endpoint + "/storage/v1/b/" + url.PathEscape(c.Bucket) + "/o/" + url.PathEscape(c.Collection+"/"+id) + "?alt=media"
	}
	upload := func(id string) string {
		return endpoint + "/upload/storage/v1/b/" + url.PathEscape(c.Bucket) + "/o?" + url.Values{"uploadType": {"media"}, "name": {c.Collection + "/" + id}, "ifGenerationMatch": {"0"}}.Encode()
	}
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint, c.Bucket, c.Collection + "/"}
	return blob.NewHTTP(blob.HTTPConfig{Client: c.Client, Authorize: authorize, ObjectURL: location, UploadURL: upload, Generation: true, ConditionalDelete: true, List: t.listBlobs})
}

func (t *transport) listBlobs(ctx context.Context, after string, n int) ([]blob.Metadata, bool, error) {
	q := url.Values{"prefix": {t.prefix}, "maxResults": {fmt.Sprint(min(n+1, 1000))}}
	if after != "" {
		q.Set("startOffset", t.prefix+after)
	}
	items := []blob.Metadata{}
	seen := map[string]bool{}
	for {
		r, err := t.client.Do(ctx, "GET", t.base()+"?"+q.Encode(), nil, nil, false)
		if err != nil {
			return nil, false, err
		}
		var out struct {
			Next  string                                    `json:"nextPageToken"`
			Items []struct{ Name, Generation, Size string } `json:"items"`
		}
		if json.Unmarshal(r.Body, &out) != nil {
			return nil, false, fmt.Errorf("yuruidb: malformed GCS blob listing")
		}
		for _, item := range out.Items {
			if !strings.HasPrefix(item.Name, t.prefix) {
				return nil, false, fmt.Errorf("yuruidb: foreign blob key")
			}
			id := strings.TrimPrefix(item.Name, t.prefix)
			if id <= after {
				continue
			}
			size, err := strconv.ParseInt(item.Size, 10, 64)
			if err != nil {
				return nil, false, fmt.Errorf("yuruidb: invalid GCS blob size")
			}
			items = append(items, blob.Metadata{ID: id, Revision: item.Generation, Size: size})
		}
		if len(items) >= n || out.Next == "" {
			more := len(items) > n || out.Next != ""
			return items[:min(n, len(items))], more, nil
		}
		if seen[out.Next] {
			return nil, false, fmt.Errorf("yuruidb: repeated GCS page token")
		}
		seen[out.Next] = true
		q.Set("pageToken", out.Next)
	}
}
