// Package firebase registers Firebase Realtime Database through REST. For
// Cloud Firestore use drivers/firestore instead.
package firebase

import (
	"bytes"
	"context"
	"encoding/hex"
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
	yuruidb.Register("firebase", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
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
	r, err := t.client.Do(ctx, "GET", t.base+"/"+t.physicalKey(key)+".json", nil, http.Header{"X-Firebase-Etag": {"true"}}, false)
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
	_, err := t.WriteItem(ctx, key, b, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, b []byte, expected string) (string, error) {
	if expected == "" {
		expected = "null_etag"
	}
	response, err := t.client.Do(ctx, "PUT", t.base+"/"+t.physicalKey(key)+".json", b, http.Header{"Content-Type": {"application/json"}, "If-Match": {expected}, "X-Firebase-ETag": {"true"}}, true)
	return response.Header.Get("ETag"), err
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	q := url.Values{"orderBy": {`"$key"`}, "limitToFirst": {fmt.Sprint(n + 2)}}
	if after != "" {
		b, _ := json.Marshal(strings.TrimPrefix(t.physicalKey(after), t.collection+"/"))
		q.Set("startAt", string(b))
	}
	var out map[string]json.RawMessage
	if err := t.client.JSON(ctx, "GET", t.base+"/"+t.collection+".json?"+q.Encode(), nil, &out, nil, false); err != nil {
		return nil, false, err
	}
	keys := []string{}
	for physical := range out {
		if !strings.HasPrefix(physical, "x") {
			return nil, false, fmt.Errorf("yuruidb: malformed Firebase physical key")
		}
		decoded, err := hex.DecodeString(physical[1:])
		if err != nil {
			return nil, false, err
		}
		key := t.collection + "/" + string(decoded)
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
	copy.collection = name
	return &copy, nil
}

func (t *transport) physicalKey(key string) string {
	if key == "" {
		return key
	}
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return key
	}
	return parts[0] + "/x" + hex.EncodeToString([]byte(parts[1]))
}

func (t *transport) Delete(ctx context.Context, key, expected string) error {
	_, err := t.client.Do(ctx, "DELETE", t.base+"/"+t.physicalKey(key)+".json", nil, http.Header{"If-Match": {expected}}, true)
	return err
}
