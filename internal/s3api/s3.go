package s3api

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	awsauth "github.com/shibukawa/yuruidb/auth/aws"
	"github.com/shibukawa/yuruidb/blob"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type Config struct {
	Endpoint, Region, Bucket, Collection string
	Client                               *http.Client
	Credentials                          awsauth.Provider
	Authorize                            auth.Authorizer
	// AddressStyle is auto (default), virtual, or path. Auto selects virtual
	// hosting for AWS endpoints and path style for custom endpoints and R2.
	AddressStyle string
	// VerifyConditions also verifies AWS endpoints; custom endpoints are always
	// checked before advertising safe conditional mutations.
	VerifyConditions bool
}
type transport struct {
	client            httpx.Client
	base, prefix      string
	conditionalDelete bool
}

func newTransport(ctx context.Context, c Config, kind string) (*transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Collection != "" {
		if err := yuruidb.ValidateCollection(c.Collection); err != nil {
			return nil, err
		}
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/\\?#") {
		return nil, fmt.Errorf("%w: bucket required", yuruidb.ErrInvalid)
	}
	custom := c.Endpoint != ""
	if c.Region == "" {
		if kind == "r2" {
			c.Region = "auto"
		} else {
			c.Region = awsauth.Region("")
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
	style := c.AddressStyle
	if style == "" || style == "auto" {
		style = "path"
		if !custom && kind == "s3" && !strings.Contains(c.Bucket, ".") {
			style = "virtual"
		}
	}
	var base string
	switch style {
	case "path":
		base = endpoint + "/" + url.PathEscape(c.Bucket)
	case "virtual":
		u, _ := url.Parse(endpoint)
		if u.Path != "" && u.Path != "/" {
			return nil, fmt.Errorf("%w: virtual endpoint must not have a path", yuruidb.ErrInvalid)
		}
		if strings.ContainsAny(c.Bucket, ".:_") || strings.ToLower(c.Bucket) != c.Bucket {
			return nil, fmt.Errorf("%w: bucket name is not suitable for virtual hosting", yuruidb.ErrInvalid)
		}
		u.Host = c.Bucket + "." + u.Host
		base = strings.TrimRight(u.String(), "/")
	default:
		return nil, fmt.Errorf("%w: invalid S3 address style", yuruidb.ErrInvalid)
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = awsauth.Signer(c.Region, "s3", c.Credentials)
	}
	prefix := ""
	if c.Collection != "" {
		prefix = c.Collection + "/"
	}
	t := &transport{client: httpx.Client{HTTP: c.Client, Auth: authorize}, base: base, prefix: prefix, conditionalDelete: kind == "s3" && !custom}
	if custom || c.VerifyConditions {
		if err := t.verify(ctx); err != nil {
			return nil, err
		}
	}
	return t, nil
}
func Open(ctx context.Context, c Config, kind string) (yuruidb.RawStore, error) {
	t, err := newTransport(ctx, c, kind)
	if err != nil {
		return nil, err
	}
	return recordstore.New(t, c.Collection, kind+":"+t.base, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}

// Verification reports observed conditions, not a proof for all possible races.
type Verification struct{ ConditionalWrites, ConditionalDelete bool }

func Verify(ctx context.Context, c Config, kind string) (Verification, error) {
	c.VerifyConditions = true
	t, err := newTransport(ctx, c, kind)
	if err != nil {
		return Verification{}, err
	}
	return Verification{true, t.conditionalDelete}, nil
}
func (t *transport) verify(ctx context.Context) (result error) {
	id, err := yuruidb.NewID()
	if err != nil {
		return err
	}
	key := t.prefix + "__yuruidb_probe_" + id
	owned := false
	defer func() {
		if !owned {
			return
		}
		_, err := t.client.Do(ctx, "DELETE", t.base+"/"+key, nil, nil, true)
		if result == nil && err != nil {
			result = fmt.Errorf("yuruidb: could not clean up S3 condition probe: %w", err)
		}
	}()
	first, err := t.WriteItem(ctx, key, []byte("probe-initial-"+id), "")
	if err != nil {
		return err
	}
	owned = true
	if first == "" {
		return fmt.Errorf("%w: S3 probe lacks ETag", yuruidb.ErrUnsupported)
	}
	if _, err = t.WriteItem(ctx, key, []byte("probe-duplicate-"+id), ""); !errors.Is(err, yuruidb.ErrConflict) {
		return fmt.Errorf("%w: endpoint does not enforce If-None-Match", yuruidb.ErrUnsupported)
	}
	next, err := t.WriteItem(ctx, key, []byte("probe-next-"+id), first)
	if err != nil {
		return err
	}
	if _, err = t.WriteItem(ctx, key, []byte("probe-stale-"+id), first); !errors.Is(err, yuruidb.ErrConflict) {
		return fmt.Errorf("%w: endpoint does not enforce If-Match", yuruidb.ErrUnsupported)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	var failure error
	for _, body := range []string{"probe-race-a-", "probe-race-b-"} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			_, err := t.WriteItem(ctx, key, []byte(body+id), next)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if !errors.Is(err, yuruidb.ErrConflict) {
				failure = err
			}
		}(body)
	}
	wg.Wait()
	if failure != nil {
		return failure
	}
	if wins != 1 {
		return fmt.Errorf("%w: endpoint failed concurrent CAS probe", yuruidb.ErrUnsupported)
	}
	_, err = t.client.Do(ctx, "DELETE", t.base+"/"+key, nil, http.Header{"If-Match": {first}}, true)
	t.conditionalDelete = errors.Is(err, yuruidb.ErrConflict)
	if err != nil && !t.conditionalDelete {
		var remote *httpx.Error
		if !errors.As(err, &remote) || (remote.Status != 400 && remote.Status != 405 && remote.Status != 501) {
			return err
		}
	}
	if t.conditionalDelete {
		current, err := t.Read(ctx, key)
		if err != nil {
			return err
		}
		if err = t.Delete(ctx, key, current.Token); err != nil {
			return err
		}
		if _, err = t.Read(ctx, key); !errors.Is(err, yuruidb.ErrNotFound) {
			return fmt.Errorf("%w: endpoint failed conditional delete probe", yuruidb.ErrUnsupported)
		}
	}
	return nil
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
func (t *transport) Write(ctx context.Context, key string, b []byte, expected string) error {
	_, err := t.WriteItem(ctx, key, b, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, data []byte, expected string) (string, error) {
	if len(data) > httpx.MaxResponse {
		return "", fmt.Errorf("%w: encoded record exceeds 32 MiB; use OpenBlobs", yuruidb.ErrInvalid)
	}

	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	if expected == "" {
		h.Set("If-None-Match", "*")
	} else {
		h.Set("If-Match", expected)
	}
	response, err := t.client.Do(ctx, "PUT", t.base+"/"+key, data, h, true)
	return response.Header.Get("ETag"), err
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

func (t *transport) ForCollection(ctx context.Context, name string) (recordstore.Transport, error) {
	copy := *t
	copy.prefix = name + "/"
	return &copy, nil
}

func (t *transport) Delete(ctx context.Context, key, expected string) error {
	if !t.conditionalDelete {
		return yuruidb.ErrUnsupported
	}
	_, err := t.client.Do(ctx, "DELETE", t.base+"/"+key, nil, http.Header{"If-Match": {expected}}, true)
	return err
}

func OpenBlobs(ctx context.Context, c Config, kind string) (*blob.Store, error) {
	if err := yuruidb.ValidateCollection(c.Collection); err != nil {
		return nil, err
	}
	t, err := newTransport(ctx, c, kind)
	if err != nil {
		return nil, err
	}
	location := func(id string) string { return t.base + "/" + t.prefix + id }
	return blob.NewHTTP(blob.HTTPConfig{Client: c.Client, Authorize: t.client.Auth, ObjectURL: location, UploadURL: location, ConditionalDelete: t.conditionalDelete})
}
