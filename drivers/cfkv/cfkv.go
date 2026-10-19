// Package cfkv registers the eventually consistent Cloudflare Workers KV
// driver. Insert/Update/Delete return ErrUnsupported: KV has no native CAS.
// Set and Remove explicitly offer unconditional, last-writer-wins mutations.
package cfkv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
)

type Config struct {
	Endpoint, AccountID, NamespaceID, Collection string
	Client                                       *http.Client
	Token                                        auth.TokenProvider
	Authorize                                    auth.Authorizer
}
type transport struct {
	client       httpx.Client
	base, prefix string
}
type Store struct {
	*recordstore.Store
	transport *transport
	prefix    string
}

func init() {
	yuruidb.Register("cfkv", func(ctx context.Context, v any) (yuruidb.Store, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return Open(ctx, c)
	})
}
func Open(ctx context.Context, c Config) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.AccountID == "" || c.NamespaceID == "" {
		return nil, fmt.Errorf("%w: KV account and namespace required", yuruidb.ErrInvalid)
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://api.cloudflare.com/client/v4"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = auth.Bearer(c.Token)
	}
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint + "/accounts/" + url.PathEscape(c.AccountID) + "/storage/kv/namespaces/" + url.PathEscape(c.NamespaceID), c.Collection + "/"}
	s, err := recordstore.New(t, c.Collection, "cfkv:"+t.base, yuruidb.Capabilities{Distributed: true})
	if err != nil {
		return nil, err
	}
	return &Store{s, t, c.Collection + "/"}, nil
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	r, err := t.client.Do(ctx, "GET", t.base+"/values/"+url.PathEscape(key), nil, nil, false)
	return recordstore.Item{Data: r.Body}, err
}
func (t *transport) Write(context.Context, string, []byte, string) error {
	return yuruidb.ErrUnsupported
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	q := url.Values{"prefix": {t.prefix}, "limit": {"1000"}}
	keys := []string{}
	seen := map[string]bool{}
	for {
		var out struct {
			Success bool `json:"success"`
			Result  []struct {
				Name string `json:"name"`
			} `json:"result"`
			Info struct {
				Cursor string `json:"cursor"`
			} `json:"result_info"`
		}
		if err := t.client.JSON(ctx, "GET", t.base+"/keys?"+q.Encode(), nil, &out, nil, false); err != nil {
			return nil, false, err
		}
		if !out.Success {
			return nil, false, fmt.Errorf("yuruidb: KV listing failed")
		}
		for _, item := range out.Result {
			if item.Name > after {
				keys = append(keys, item.Name)
			}
		}
		if len(keys) > n {
			return keys[:n], true, nil
		}
		if out.Info.Cursor == "" {
			return keys, false, nil
		}
		if seen[out.Info.Cursor] {
			return nil, false, fmt.Errorf("yuruidb: repeated KV page cursor")
		}
		seen[out.Info.Cursor] = true
		q.Set("cursor", out.Info.Cursor)
	}
}
func (t *transport) Close() error { return nil }

// Set unconditionally creates or replaces a value. Its returned revision
// identifies that value but cannot be used for a conditional KV mutation.
func (s *Store) Set(ctx context.Context, id string, value []byte) (yuruidb.Record, error) {
	if err := s.Check(ctx); err != nil {
		return yuruidb.Record{}, err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return yuruidb.Record{}, err
	}
	revision, err := yuruidb.NewRevision()
	if err != nil {
		return yuruidb.Record{}, err
	}
	e := recordstore.Envelope{ID: id, Value: value, Revision: revision}
	b, err := json.Marshal(e)
	if err != nil {
		return yuruidb.Record{}, err
	}
	err = s.transport.mutate(ctx, "PUT", s.prefix+id, b)
	if err != nil {
		return yuruidb.Record{}, err
	}
	return yuruidb.Record{ID: id, Value: append([]byte(nil), value...), Revision: revision}, nil
}

// Remove is unconditional and may race writers. Reads may remain stale.
func (s *Store) Remove(ctx context.Context, id string) error {
	if err := s.Check(ctx); err != nil {
		return err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return err
	}
	return s.transport.mutate(ctx, "DELETE", s.prefix+id, nil)
}

func (t *transport) mutate(ctx context.Context, method, key string, body []byte) error {
	response, err := t.client.Do(ctx, method, t.base+"/values/"+url.PathEscape(key), body, http.Header{"Content-Type": {"application/octet-stream"}}, true)
	if err != nil {
		return err
	}
	var out struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(response.Body, &out) != nil || out.Success == nil {
		return errors.Join(yuruidb.ErrOutcomeUnknown, fmt.Errorf("yuruidb: malformed KV mutation response"))
	}
	if !*out.Success {
		return fmt.Errorf("yuruidb: KV mutation failed")
	}
	return nil
}
