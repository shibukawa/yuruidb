// Package memory registers the in-process "memory" driver.
package memory

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"sort"
	"sync"
)

type Config struct{ Collection string }
type transport struct {
	sync.RWMutex
	data   map[string]recordstore.Item
	closed bool
}

func init() {
	yuruidb.Register("memory", func(ctx context.Context, v any) (yuruidb.Store, error) {
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
	return recordstore.New(&transport{data: map[string]recordstore.Item{}}, c.Collection, "memory", yuruidb.Capabilities{ConditionalMutations: true, StrongReads: true})
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
	if err := ctx.Err(); err != nil {
		return err
	}
	t.Lock()
	defer t.Unlock()
	if t.closed {
		return yuruidb.ErrClosed
	}
	old, ok := t.data[k]
	if ok && old.Token != expected || !ok && expected != "" {
		return yuruidb.ErrConflict
	}
	token, err := yuruidb.NewRevision()
	if err != nil {
		return err
	}
	t.data[k] = recordstore.Item{Data: append([]byte(nil), b...), Token: token}
	return nil
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
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
		if k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more, nil
}
func (t *transport) Close() error { t.Lock(); defer t.Unlock(); t.closed = true; return nil }
