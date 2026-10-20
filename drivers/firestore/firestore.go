// Package firestore registers Cloud Firestore through its REST API.
package firestore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	Endpoint, Project, Database, Collection string
	Client                                  *http.Client
	Token                                   auth.TokenProvider
	Authorize                               auth.Authorizer
}
type field struct {
	Bytes string `json:"bytesValue"`
}
type document struct {
	Name       string           `json:"name,omitempty"`
	Fields     map[string]field `json:"fields"`
	UpdateTime string           `json:"updateTime,omitempty"`
}
type transport struct {
	client                       httpx.Client
	endpoint, parent, collection string
}

func init() {
	yuruidb.Register("firestore", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
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
	if c.Project == "" {
		return nil, fmt.Errorf("%w: Firestore project required", yuruidb.ErrInvalid)
	}
	if err := yuruidb.ValidateCollection(c.Project); err != nil {
		return nil, err
	}
	if c.Database != "" && c.Database != "(default)" {
		if err := yuruidb.ValidateCollection(c.Database); err != nil {
			return nil, err
		}
	}
	if c.Database == "" {
		c.Database = "(default)"
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://firestore.googleapis.com/v1"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = auth.Bearer(c.Token)
	}
	parent := "projects/" + c.Project + "/databases/" + c.Database + "/documents"
	t := &transport{httpx.Client{HTTP: c.Client, Auth: authorize}, endpoint, parent, c.Collection}
	return recordstore.New(t, c.Collection, "firestore:"+endpoint+"/"+parent, yuruidb.Capabilities{ConditionalMutations: true, Distributed: true, StrongReads: true})
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	var doc document
	err := t.client.JSON(ctx, "GET", t.endpoint+"/"+t.parent+"/"+key, nil, &doc, nil, false)
	if err != nil {
		return recordstore.Item{}, err
	}
	b, err := base64.StdEncoding.DecodeString(doc.Fields["payload"].Bytes)
	if err != nil || doc.UpdateTime == "" {
		return recordstore.Item{}, fmt.Errorf("yuruidb: malformed Firestore document")
	}
	return recordstore.Item{Data: b, Token: doc.UpdateTime}, nil
}
func (t *transport) Write(ctx context.Context, key string, b []byte, expected string) error {
	_, err := t.WriteItem(ctx, key, b, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, b []byte, expected string) (string, error) {
	q := url.Values{}
	if expected == "" {
		q.Set("currentDocument.exists", "false")
	} else {
		q.Set("currentDocument.updateTime", expected)
	}
	doc := document{Fields: map[string]field{"payload": {Bytes: base64.StdEncoding.EncodeToString(b)}}}
	body, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	response, err := t.client.Do(ctx, "PATCH", t.endpoint+"/"+t.parent+"/"+key+"?"+q.Encode(), body, http.Header{"Content-Type": {"application/json"}}, true)
	if err != nil {
		var failure struct {
			Error struct {
				Status string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(response.Body, &failure) == nil && failure.Error.Status == "FAILED_PRECONDITION" {
			return "", errors.Join(yuruidb.ErrConflict, err)
		}
	}
	if err != nil {
		return "", err
	}
	var out document
	if json.Unmarshal(response.Body, &out) != nil || out.UpdateTime == "" {
		return "", fmt.Errorf("%w: missing Firestore updateTime", yuruidb.ErrOutcomeUnknown)
	}
	return out.UpdateTime, nil
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	entries, more, err := t.Scan(ctx, after, n)
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, more, err
}
func (t *transport) Scan(ctx context.Context, after string, n int) ([]recordstore.Entry, bool, error) {
	query := map[string]any{"from": []any{map[string]any{"collectionId": t.collection}}, "orderBy": []any{map[string]any{"field": map[string]any{"fieldPath": "__name__"}, "direction": "ASCENDING"}}, "limit": n + 1}
	if after != "" {
		query["startAt"] = map[string]any{"values": []any{map[string]any{"referenceValue": t.parent + "/" + after}}, "before": false}
	}
	var out []struct {
		Document *document `json:"document"`
	}
	if err := t.client.JSON(ctx, "POST", t.endpoint+"/"+t.parent+":runQuery", map[string]any{"structuredQuery": query}, &out, nil, false); err != nil {
		return nil, false, err
	}
	entries := []recordstore.Entry{}
	for _, r := range out {
		if r.Document != nil {
			prefix := t.parent + "/"
			if !strings.HasPrefix(r.Document.Name, prefix) {
				return nil, false, fmt.Errorf("yuruidb: foreign Firestore document")
			}
			b, err := base64.StdEncoding.DecodeString(r.Document.Fields["payload"].Bytes)
			if err != nil {
				return nil, false, err
			}
			entries = append(entries, recordstore.Entry{Key: strings.TrimPrefix(r.Document.Name, prefix), Item: recordstore.Item{Data: b, Token: r.Document.UpdateTime}})
		}
	}
	more := len(entries) > n
	if more {
		entries = entries[:n]
	}
	return entries, more, nil
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

func (t *transport) Delete(ctx context.Context, key, expected string) error {
	response, err := t.client.Do(ctx, "DELETE", t.endpoint+"/"+t.parent+"/"+key+"?"+url.Values{"currentDocument.updateTime": {expected}}.Encode(), nil, nil, true)
	if err != nil {
		var remote struct {
			Error struct {
				Status string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(response.Body, &remote) == nil && remote.Error.Status == "FAILED_PRECONDITION" {
			return errors.Join(yuruidb.ErrConflict, err)
		}
	}
	return err
}
