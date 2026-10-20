// Package bbolt registers the local-file bbolt driver.
package bbolt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	bolt "go.etcd.io/bbolt"
)

type Config struct {
	Path, Collection string
	Mode             os.FileMode
	Timeout          time.Duration
}
type connection struct {
	db      *bolt.DB
	refs    int
	mode    os.FileMode
	timeout time.Duration
}

var connections = struct {
	sync.Mutex
	files map[string]*connection
}{files: make(map[string]*connection)}

type transport struct {
	db     *bolt.DB
	bucket []byte
	path   string
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
func canonicalPath(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(p)), nil
}
func openTransport(ctx context.Context, c Config) (*transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Collection != "" {
		if err := yuruidb.ValidateCollection(c.Collection); err != nil {
			return nil, err
		}
	}
	if c.Path == "" {
		return nil, fmt.Errorf("%w: bbolt path required", yuruidb.ErrInvalid)
	}
	path, err := canonicalPath(c.Path)
	if err != nil {
		return nil, err
	}
	mode := c.Mode
	if mode == 0 {
		mode = 0600
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = time.Second
	}
	connections.Lock()
	conn := connections.files[path]
	if conn != nil {
		if conn.mode != mode || conn.timeout != timeout {
			connections.Unlock()
			return nil, fmt.Errorf("%w: conflicting bbolt open options", yuruidb.ErrInvalid)
		}
		conn.refs++
	} else {
		db, err := bolt.Open(path, mode, &bolt.Options{Timeout: timeout})
		if err != nil {
			connections.Unlock()
			return nil, err
		}
		// This is the first owner of the file: no live upload can use its staging
		// buckets. Recover abandoned uploads before publishing the connection.
		if err := cleanupBlobStaging(db); err != nil {
			db.Close()
			connections.Unlock()
			return nil, err
		}
		conn = &connection{db: db, refs: 1, mode: mode, timeout: timeout}
		connections.files[path] = conn
	}
	connections.Unlock()
	t := &transport{db: conn.db, path: path}
	if c.Collection != "" {
		t.bucket = []byte(c.Collection)
		if err = t.ensureBucket(ctx); err != nil {
			t.Close()
			return nil, err
		}
	}
	return t, nil
}

func OpenRaw(ctx context.Context, c Config) (yuruidb.RawStore, error) {
	t, err := openTransport(ctx, c)
	if err != nil {
		return nil, err
	}
	s, err := recordstore.New(t, c.Collection, "bbolt:"+t.path, yuruidb.Capabilities{ConditionalMutations: true, StrongReads: true})
	if err != nil {
		t.Close()
	}
	return s, err
}
func (t *transport) ensureBucket(ctx context.Context) error {
	return mapError(t.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(t.bucket)
		return err
	}))
}
func (t *transport) ForCollection(ctx context.Context, name string) (recordstore.Transport, error) {
	copy := *t
	copy.bucket = []byte(name)
	if err := copy.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return &copy, nil
}
func decodeItem(data []byte, key string) (recordstore.Item, error) {
	if !bytes.HasPrefix(data, []byte("YRE1")) {
		return recordstore.Item{}, fmt.Errorf("yuruidb: corrupt bbolt envelope")
	}
	e, err := recordstore.Decode(data, strings.SplitN(key, "/", 2)[1])
	return recordstore.Item{Data: append([]byte(nil), data...), Token: e.Revision}, err
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	if err := ctx.Err(); err != nil {
		return recordstore.Item{}, err
	}
	var item recordstore.Item
	err := t.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(t.bucket).Get([]byte(key))
		if v == nil {
			return yuruidb.ErrNotFound
		}
		var err error
		item, err = decodeItem(v, key)
		return err
	})
	return item, mapError(err)
}
func (t *transport) Write(ctx context.Context, key string, data []byte, expected string) error {
	_, err := t.WriteItem(ctx, key, data, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, data []byte, expected string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e, err := recordstore.Decode(data, strings.SplitN(key, "/", 2)[1])
	if err != nil {
		return "", err
	}
	err = t.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(t.bucket)
		v := bucket.Get([]byte(key))
		if v != nil {
			old, err := decodeItem(v, key)
			if err != nil {
				return err
			}
			if old.Token != expected {
				return yuruidb.ErrConflict
			}
		} else if expected != "" {
			return yuruidb.ErrConflict
		}
		return bucket.Put([]byte(key), recordstore.BinaryEnvelope(e))
	})
	return e.Revision, mapError(err)
}
func (t *transport) Delete(ctx context.Context, key, expected string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return mapError(t.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket(t.bucket)
		v := bucket.Get([]byte(key))
		if v == nil {
			return yuruidb.ErrConflict
		}
		old, err := decodeItem(v, key)
		if err != nil {
			return err
		}
		if old.Token != expected {
			return yuruidb.ErrConflict
		}
		return bucket.Delete([]byte(key))
	}))
}
func (t *transport) Scan(ctx context.Context, after string, n int) ([]recordstore.Entry, bool, error) {
	entries := []recordstore.Entry{}
	err := t.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(t.bucket).Cursor()
		k, v := c.Seek([]byte(after))
		if bytes.Equal(k, []byte(after)) {
			k, v = c.Next()
		}
		for ; k != nil && len(entries) <= n; k, v = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			item, err := decodeItem(v, string(k))
			if err != nil {
				return err
			}
			entries = append(entries, recordstore.Entry{Key: string(k), Item: item})
		}
		return nil
	})
	more := len(entries) > n
	if more {
		entries = entries[:n]
	}
	return entries, more, mapError(err)
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	entries, more, err := t.Scan(ctx, after, n)
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, more, err
}
func (t *transport) Close() error {
	connections.Lock()
	defer connections.Unlock()
	conn := connections.files[t.path]
	if conn == nil {
		return nil
	}
	conn.refs--
	if conn.refs == 0 {
		delete(connections.files, t.path)
		return mapError(conn.db.Close())
	}
	return nil
}
func mapError(err error) error {
	if errors.Is(err, bolt.ErrDatabaseNotOpen) {
		return yuruidb.ErrClosed
	}
	return err
}

type backupWriter struct {
	ctx context.Context
	w   io.Writer
	err error
}

func (w *backupWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	n, err := w.w.Write(p)
	if n < len(p) && err == nil {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func (t *transport) Backup(ctx context.Context, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return mapError(t.db.View(func(tx *bolt.Tx) error {
		writer := &backupWriter{ctx: ctx, w: w}
		_, err := tx.WriteTo(writer)
		// bbolt formats some WriteTo errors without wrapping their causes.
		if writer.err != nil {
			return fmt.Errorf("yuruidb: bbolt backup: %w", writer.err)
		}
		return err
	}))
}
func Open(ctx context.Context, c Config) (*yuruidb.Store, error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap(raw), nil
}
