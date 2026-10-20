// Package memory registers the in-process memory driver.
package memory

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"sort"
	"strings"
	"sync"
)

type Config struct{ Collection string }
type state struct {
	sync.RWMutex
	data   map[string]recordstore.Item
	closed bool
}
type transport struct {
	*state
	prefix string
}

func init() {
	yuruidb.Register("memory", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
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
	identity, err := yuruidb.NewRevision()
	if err != nil {
		return nil, err
	}
	return recordstore.New(&transport{state: &state{data: make(map[string]recordstore.Item)}, prefix: c.Collection + "/"}, c.Collection, "memory:"+identity, yuruidb.Capabilities{ConditionalMutations: true, StrongReads: true})
}
func (t *transport) ForCollection(ctx context.Context, name string) (recordstore.Transport, error) {
	return &transport{state: t.state, prefix: name + "/"}, nil
}
func (t *transport) Read(ctx context.Context, k string) (recordstore.Item, error) {
	if err := ctx.Err(); err != nil {
		return recordstore.Item{}, err
	}
	t.RLock()
	defer t.RUnlock()
	if t.closed {
		return recordstore.Item{}, yuruidb.ErrClosed
	}
	i, ok := t.data[k]
	if !ok {
		return i, yuruidb.ErrNotFound
	}
	i.Data = append([]byte(nil), i.Data...)
	return i, nil
}
func (t *transport) Write(ctx context.Context, k string, b []byte, expected string) error {
	_, err := t.WriteItem(ctx, k, b, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, k string, b []byte, expected string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	t.Lock()
	defer t.Unlock()
	if t.closed {
		return "", yuruidb.ErrClosed
	}
	old, ok := t.data[k]
	if ok && old.Token != expected || !ok && expected != "" {
		return "", yuruidb.ErrConflict
	}
	token, err := yuruidb.NewRevision()
	if err != nil {
		return "", err
	}
	t.data[k] = recordstore.Item{Data: append([]byte(nil), b...), Token: token}
	return token, nil
}
func (t *transport) Delete(ctx context.Context, k, expected string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.Lock()
	defer t.Unlock()
	if t.closed {
		return yuruidb.ErrClosed
	}
	old, ok := t.data[k]
	if !ok || old.Token != expected {
		return yuruidb.ErrConflict
	}
	delete(t.data, k)
	return nil
}
func (t *transport) Scan(ctx context.Context, after string, n int) ([]recordstore.Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	t.RLock()
	defer t.RUnlock()
	if t.closed {
		return nil, false, yuruidb.ErrClosed
	}
	keys := []string{}
	for k := range t.data {
		if strings.HasPrefix(k, t.prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	out := make([]recordstore.Entry, len(keys))
	for i, k := range keys {
		v := t.data[k]
		v.Data = append([]byte(nil), v.Data...)
		out[i] = recordstore.Entry{Key: k, Item: v}
	}
	return out, more, nil
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	entries, more, err := t.Scan(ctx, after, n)
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, more, err
}
func (t *transport) Close() error { t.Lock(); defer t.Unlock(); t.closed = true; return nil }
func Open(ctx context.Context, c Config) (*yuruidb.Store, error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap(raw), nil
}
