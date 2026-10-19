// Package firebase registers Firebase Realtime Database through REST. For
// Cloud Firestore use drivers/firestore instead.
package firebase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type Config struct {
	Endpoint, Collection string
	Client               *http.Client
	Token                auth.TokenProvider
	Authorize            auth.Authorizer
	IDToken              bool
}
type transport struct {
	client           httpx.Client
	base, collection string
}

func init() {
	yuruidb.Register("firebase", func(ctx context.Context, v any) (yuruidb.Store, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return Open(ctx, c)
	})
}
func Open(ctx context.Context, c Config) (yuruidb.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		if c.IDToken {
			authorize = auth.QueryToken("auth", c.Token)
		} else {
			authorize = auth.Bearer(c.Token)
		}
	}
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint, c.Collection}
	return recordstore.New(t, c.Collection, "firebase:"+endpoint, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	r, err := t.client.Do(ctx, "GET", t.base+"/"+key+".json", nil, http.Header{"X-Firebase-Etag": {"true"}}, false)
	if err != nil {
		return recordstore.Item{}, err
	}
	if bytes.Equal(bytes.TrimSpace(r.Body), []byte("null")) {
		return recordstore.Item{}, yuruidb.ErrNotFound
	}
	token := r.Header.Get("ETag")
	if token == "" {
		return recordstore.Item{}, fmt.Errorf("yuruidb: Firebase response lacks ETag")
	}
	return recordstore.Item{Data: r.Body, Token: token}, nil
}
func (t *transport) Write(ctx context.Context, key string, b []byte, expected string) error {
	if expected == "" {
		expected = "null_etag"
	}
	_, err := t.client.Do(ctx, "PUT", t.base+"/"+key+".json", b, http.Header{"Content-Type": {"application/json"}, "If-Match": {expected}}, true)
	return err
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	q := url.Values{"orderBy": {`"$key"`}, "limitToFirst": {fmt.Sprint(n + 2)}}
	if after != "" {
		b, _ := json.Marshal(strings.TrimPrefix(after, t.collection+"/"))
		q.Set("startAt", string(b))
	}
	var out map[string]json.RawMessage
	if err := t.client.JSON(ctx, "GET", t.base+"/"+t.collection+".json?"+q.Encode(), nil, &out, nil, false); err != nil {
		return nil, false, err
	}
	keys := []string{}
	for id := range out {
		key := t.collection + "/" + id
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more, nil
}
func (t *transport) Close() error { return nil }
