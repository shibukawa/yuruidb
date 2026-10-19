package s3api

import (
	"context"
	"encoding/xml"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	Endpoint, Region, Bucket, Collection string
	Client                               *http.Client
	Credentials                          awsauth.Provider
	Authorize                            auth.Authorizer
}
type transport struct {
	client       httpx.Client
	base, prefix string
}

func Open(ctx context.Context, c Config, kind string) (yuruidb.RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/\\?#") {
		return nil, fmt.Errorf("%w: bucket required", yuruidb.ErrInvalid)
	}
	if c.Region == "" {
		if kind == "r2" {
			c.Region = "auto"
		} else {
			c.Region = "us-east-1"
		}
	}
	if c.Endpoint == "" {
		if kind == "r2" {
			return nil, fmt.Errorf("%w: R2 endpoint required", yuruidb.ErrInvalid)
		}
		c.Endpoint = "https://s3." + c.Region + ".amazonaws.com"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = awsauth.Signer(c.Region, "s3", c.Credentials)
	}
	t := &transport{client: httpx.Client{HTTP: c.Client, Auth: authorize}, base: endpoint + "/" + url.PathEscape(c.Bucket), prefix: c.Collection + "/"}
	return recordstore.New(t, c.Collection, kind+":"+t.base, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	r, err := t.client.Do(ctx, "GET", t.base+"/"+key, nil, nil, false)
	if err != nil {
		return recordstore.Item{}, err
	}
	token := r.Header.Get("ETag")
	if token == "" {
		return recordstore.Item{}, fmt.Errorf("yuruidb: object response lacks ETag")
	}
	return recordstore.Item{Data: r.Body, Token: token}, nil
}
func (t *transport) Write(ctx context.Context, key string, data []byte, expected string) error {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if expected == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", expected)
	}
	_, err := t.client.Do(ctx, "PUT", t.base+"/"+key, data, h, true)
	return err
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	if n > 1000 {
		n = 1000
	}
	q := url.Values{"list-type": {"2"}, "prefix": {t.prefix}, "max-keys": {fmt.Sprint(n)}}
	if after != "" {
		q.Set("start-after", after)
	}
	r, err := t.client.Do(ctx, "GET", t.base+"?"+q.Encode(), nil, nil, false)
	if err != nil {
		return nil, false, err
	}
	var out struct {
		Truncated bool `xml:"IsTruncated"`
		Objects   []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err = xml.Unmarshal(r.Body, &out); err != nil {
		return nil, false, fmt.Errorf("yuruidb: malformed S3 listing: %w", err)
	}
	keys := make([]string, len(out.Objects))
	for i, v := range out.Objects {
		keys[i] = v.Key
	}
	return keys, out.Truncated, nil
}
func (t *transport) Close() error { return nil }
