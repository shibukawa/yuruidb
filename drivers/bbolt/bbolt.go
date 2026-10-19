// Package bbolt registers the local-file "bbolt" driver. It is not a shared
// remote backend for multiple Lambda processes.
package bbolt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	bolt "go.etcd.io/bbolt"
	"os"
	"time"
)

type Config struct {
	Path, Collection string
	Mode             os.FileMode
	Timeout          time.Duration
}
type transport struct {
	db     *bolt.DB
	bucket []byte
}

func init() {
	yuruidb.Register("bbolt", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
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
	if err := yuruidb.ValidateCollection(c.Collection); err != nil {
		return nil, err
	}
	if c.Path == "" {
		return nil, fmt.Errorf("%w: bbolt path required", yuruidb.ErrInvalid)
	}
	mode := c.Mode
	if mode == 0 {
		mode = 0600
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = time.Second
	}
	db, err := bolt.Open(c.Path, mode, &bolt.Options{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	t := &transport{db: db, bucket: []byte(c.Collection)}
	if err = db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(t.bucket); return err }); err != nil {
		db.Close()
		return nil, err
	}
	s, err := recordstore.New(t, c.Collection, "bbolt:"+c.Path, yuruidb.Capabilities{ConditionalMutations: true, StrongReads: true})
	if err != nil {
		db.Close()
	}
	return s, err
}
func (t *transport) Read(ctx context.Context, k string) (recordstore.Item, error) {
	var i recordstore.Item
	if err := ctx.Err(); err != nil {
		return i, err
	}
	err := t.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(t.bucket).Get([]byte(k))
		if v == nil {
			return yuruidb.ErrNotFound
		}
		return json.Unmarshal(v, &i)
	})
	return i, mapError(err)
}
func (t *transport) Write(ctx context.Context, k string, b []byte, expected string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return mapError(t.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(t.bucket)
		v := bucket.Get([]byte(k))
		var old recordstore.Item
		if v != nil {
			if err := json.Unmarshal(v, &old); err != nil {
				return err
			}
			if old.Token != expected {
				return yuruidb.ErrConflict
			}
		} else if expected != "" {
			return yuruidb.ErrConflict
		}
		token, err := yuruidb.NewRevision()
		if err != nil {
			return err
		}
		data, err := json.Marshal(recordstore.Item{Data: b, Token: token})
		if err != nil {
			return err
		}
		return bucket.Put([]byte(k), data)
	}))
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	keys := []string{}
	err := t.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(t.bucket).Cursor()
		k, _ := c.Seek([]byte(after))
		if bytes.Equal(k, []byte(after)) {
			k, _ = c.Next()
		}
		for ; k != nil && len(keys) <= n; k, _ = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			keys = append(keys, string(k))
		}
		return nil
	})
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more, mapError(err)
}
func (t *transport) Close() error { return mapError(t.db.Close()) }
func mapError(err error) error {
	if errors.Is(err, bolt.ErrDatabaseNotOpen) {
		return yuruidb.ErrClosed
	}
	return err
}

// Open opens a JSON-typed collection using this driver.
func Open[T any](ctx context.Context, c Config) (*yuruidb.Store[T], error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap[T](raw), nil
}
