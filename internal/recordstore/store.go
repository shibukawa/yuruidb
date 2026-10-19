// Package recordstore implements portable record semantics on an atomic
// conditional key store. Object backends retain tombstones; physical garbage
// collection must not race conditional writers.
package recordstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"strings"
	"sync"
	"sync/atomic"
)

type Item struct {
	Data  []byte
	Token string
}

// Empty expected token means create-if-absent, not unconditional overwrite.
type Transport interface {
	Read(context.Context, string) (Item, error)
	Write(context.Context, string, []byte, string) error
	Keys(context.Context, string, int) ([]string, bool, error)
	Close() error
}
type Envelope struct {
	ID       string `json:"id"`
	Value    []byte `json:"value,omitempty"`
	Revision string `json:"revision"`
	Deleted  bool   `json:"deleted,omitempty"`
}
type Store struct {
	closed        atomic.Bool
	closeOnce     sync.Once
	closeErr      error
	transport     Transport
	prefix, scope string
	caps          yuruidb.Capabilities
}

func New(t Transport, collection, scope string, caps yuruidb.Capabilities) (*Store, error) {
	if err := yuruidb.ValidateCollection(collection); err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(scope + "/" + collection))
	return &Store{transport: t, prefix: collection + "/", scope: hex.EncodeToString(h[:]), caps: caps}, nil
}

// Check checks whether a store is closed or its context cancelled.
func (s *Store) Check(ctx context.Context) error {
	if s.closed.Load() {
		return yuruidb.ErrClosed
	}
	return ctx.Err()
}
func (s *Store) Capabilities() yuruidb.Capabilities { return s.caps }
func (s *Store) Close() error {
	s.closed.Store(true)
	s.closeOnce.Do(func() { s.closeErr = s.transport.Close() })
	return s.closeErr
}
func Decode(data []byte, id string) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("yuruidb: corrupt record: %w", err)
	}
	if e.ID != id || e.Revision == "" {
		return e, fmt.Errorf("yuruidb: corrupt record identity or revision")
	}
	return e, nil
}
func record(e Envelope) yuruidb.RawRecord {
	return yuruidb.RawRecord{ID: e.ID, Value: append([]byte(nil), e.Value...), Revision: e.Revision}
}
func (s *Store) read(ctx context.Context, id string) (Envelope, string, error) {
	if s.closed.Load() {
		return Envelope{}, "", yuruidb.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Envelope{}, "", err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return Envelope{}, "", err
	}
	item, err := s.transport.Read(ctx, s.prefix+id)
	if err != nil {
		return Envelope{}, "", err
	}
	e, err := Decode(item.Data, id)
	return e, item.Token, err
}
func (s *Store) Get(ctx context.Context, id string) (yuruidb.RawRecord, error) {
	e, _, err := s.read(ctx, id)
	if err == nil && e.Deleted {
		err = yuruidb.ErrNotFound
	}
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	return record(e), nil
}
func (s *Store) Insert(ctx context.Context, id string, value []byte) (yuruidb.RawRecord, error) {
	if !s.caps.ConditionalMutations {
		return yuruidb.RawRecord{}, yuruidb.ErrUnsupported
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return yuruidb.RawRecord{}, err
	}
	old, token, err := s.read(ctx, id)
	if err == nil && !old.Deleted {
		return yuruidb.RawRecord{}, yuruidb.ErrAlreadyExists
	}
	if err != nil && !errors.Is(err, yuruidb.ErrNotFound) {
		return yuruidb.RawRecord{}, err
	}
	if errors.Is(err, yuruidb.ErrNotFound) {
		token = ""
	}
	rev, err := yuruidb.NewRevision()
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	e := Envelope{ID: id, Value: value, Revision: rev}
	b, err := json.Marshal(e)
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	if err = s.transport.Write(ctx, s.prefix+id, b, token); err != nil {
		if errors.Is(err, yuruidb.ErrConflict) {
			err = yuruidb.ErrAlreadyExists
		}
		return yuruidb.RawRecord{}, err
	}
	return record(e), nil
}
func (s *Store) mutate(ctx context.Context, id string, value []byte, expected string, deleted bool) (yuruidb.RawRecord, error) {
	if !s.caps.ConditionalMutations {
		return yuruidb.RawRecord{}, yuruidb.ErrUnsupported
	}
	if expected == "" {
		return yuruidb.RawRecord{}, fmt.Errorf("%w: expected revision required", yuruidb.ErrInvalid)
	}
	old, token, err := s.read(ctx, id)
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	if old.Deleted {
		return yuruidb.RawRecord{}, yuruidb.ErrNotFound
	}
	if old.Revision != expected {
		return yuruidb.RawRecord{}, yuruidb.ErrConflict
	}
	if token == "" {
		return yuruidb.RawRecord{}, fmt.Errorf("yuruidb: backend omitted concurrency token")
	}
	rev, err := yuruidb.NewRevision()
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	e := Envelope{ID: id, Value: value, Revision: rev, Deleted: deleted}
	b, err := json.Marshal(e)
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	if err = s.transport.Write(ctx, s.prefix+id, b, token); err != nil {
		return yuruidb.RawRecord{}, err
	}
	return record(e), nil
}
func (s *Store) Update(ctx context.Context, id string, value []byte, revision string) (yuruidb.RawRecord, error) {
	return s.mutate(ctx, id, value, revision, false)
}
func (s *Store) Delete(ctx context.Context, id, revision string) error {
	_, err := s.mutate(ctx, id, nil, revision, true)
	return err
}

type cursor struct {
	Scope string `json:"s"`
	After string `json:"a"`
}

func (s *Store) List(ctx context.Context, opts yuruidb.ListOptions) (yuruidb.RawPage, error) {
	if s.closed.Load() {
		return yuruidb.RawPage{}, yuruidb.ErrClosed
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return yuruidb.RawPage{}, fmt.Errorf("%w: limit must be 1..1000", yuruidb.ErrInvalid)
	}
	after := ""
	if opts.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(opts.Cursor)
		var c cursor
		if err != nil || json.Unmarshal(b, &c) != nil || c.Scope != s.scope || !strings.HasPrefix(c.After, s.prefix) || yuruidb.ValidateID(strings.TrimPrefix(c.After, s.prefix)) != nil {
			return yuruidb.RawPage{}, fmt.Errorf("%w: cursor does not belong to this store", yuruidb.ErrInvalid)
		}
		after = c.After
	}
	page := yuruidb.RawPage{Records: make([]yuruidb.RawRecord, 0, limit)}
	for {
		if err := ctx.Err(); err != nil {
			return yuruidb.RawPage{}, err
		}
		keys, more, err := s.transport.Keys(ctx, after, limit+1)
		if err != nil {
			return yuruidb.RawPage{}, err
		}
		if len(keys) == 0 {
			if more {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: non-progressing backend listing")
			}
			return page, nil
		}
		for i, key := range keys {
			if !strings.HasPrefix(key, s.prefix) || key <= after {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: invalid backend key ordering")
			}
			after = key
			id := strings.TrimPrefix(key, s.prefix)
			if err := yuruidb.ValidateID(id); err != nil {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: foreign key in collection: %w", err)
			}
			r, err := s.Get(ctx, id)
			if err != nil && !errors.Is(err, yuruidb.ErrNotFound) {
				return yuruidb.RawPage{}, err
			}
			if err == nil {
				page.Records = append(page.Records, r)
			}
			if len(page.Records) == limit {
				if more || i+1 < len(keys) {
					b, _ := json.Marshal(cursor{s.scope, after})
					page.Cursor = base64.RawURLEncoding.EncodeToString(b)
				}
				return page, nil
			}
		}
		if !more {
			return page, nil
		}
	}
}
