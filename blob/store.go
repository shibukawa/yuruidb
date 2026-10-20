package blob

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/shibukawa/yuruidb"
)

// Backend is the driver boundary for immutable documents. List returns at most
// limit metadata entries in ascending ID byte order, strictly after after.
type Backend interface {
	Insert(context.Context, string, io.Reader) (Metadata, error)
	Open(context.Context, string) (*Reader, error)
	Delete(context.Context, string, string) error
	Remove(context.Context, string) error
	List(context.Context, string, int) ([]Metadata, bool, error)
	Close() error
}

// Store owns one explicitly named collection of immutable documents.
type Store struct {
	backend  Backend
	scope    string
	closed   atomic.Bool
	once     sync.Once
	closeErr error
}

// NewBackend takes ownership of a backend. Scope must uniquely identify its
// storage location and collection so cursors cannot cross collections.
func NewBackend(backend Backend, scope string) (*Store, error) {
	if backend == nil || scope == "" {
		return nil, yuruidb.ErrInvalid
	}
	return &Store{backend: backend, scope: scope}, nil
}

func (s *Store) check(ctx context.Context) error {
	if s.closed.Load() {
		return yuruidb.ErrClosed
	}
	return ctx.Err()
}

func (s *Store) Insert(ctx context.Context, id string, source io.Reader) (Metadata, error) {
	if err := s.check(ctx); err != nil {
		return Metadata{}, err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return Metadata{}, err
	}
	if source == nil {
		return Metadata{}, yuruidb.ErrInvalid
	}
	return s.backend.Insert(ctx, id, source)
}

// Open returns a streaming reader supporting concurrent ReadAt calls. Range
// reads use the version observed at Open and fail if it has been replaced.
// Keep ctx alive until Close; the caller must close the reader.
func (s *Store) Open(ctx context.Context, id string) (*Reader, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return nil, err
	}
	return s.backend.Open(ctx, id)
}

// Delete requires the native version returned by Open, Insert, or List.
// Unsupported conditional deletion returns ErrUnsupported; use Remove explicitly
// if the application guarantees that IDs are never reused.
func (s *Store) Delete(ctx context.Context, id, revision string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return err
	}
	if revision == "" {
		return yuruidb.ErrInvalid
	}
	return s.backend.Delete(ctx, id, revision)
}

// Remove unconditionally deletes an ID. Use only for IDs that are never reused;
// it deliberately provides no protection against deleting a replacement.
func (s *Store) Remove(ctx context.Context, id string) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return err
	}
	return s.backend.Remove(ctx, id)
}

type Page struct {
	Blobs  []Metadata
	Cursor string
}
type cursor struct{ Scope, After string }

// List enumerates potential orphans without reading document bodies. Cursors
// belong to one collection. Pages are not snapshots across concurrent changes.
func (s *Store) List(ctx context.Context, opts yuruidb.ListOptions) (Page, error) {
	if err := s.check(ctx); err != nil {
		return Page{}, err
	}
	limit := opts.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return Page{}, yuruidb.ErrInvalid
	}
	after := ""
	if opts.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(opts.Cursor)
		var c cursor
		if err != nil || json.Unmarshal(b, &c) != nil || c.Scope != s.scope || yuruidb.ValidateID(c.After) != nil {
			return Page{}, fmt.Errorf("%w: invalid blob cursor", yuruidb.ErrInvalid)
		}
		after = c.After
	}
	items, more, err := s.backend.List(ctx, after, limit)
	if err != nil {
		return Page{}, err
	}
	if len(items) > limit || (more && len(items) == 0) {
		return Page{}, fmt.Errorf("yuruidb: invalid blob listing")
	}
	for _, item := range items {
		if item.ID <= after || yuruidb.ValidateID(item.ID) != nil || item.Revision == "" || item.Size < 0 {
			return Page{}, fmt.Errorf("yuruidb: invalid blob metadata or ordering")
		}
		after = item.ID
	}
	page := Page{Blobs: items}
	if more {
		b, _ := json.Marshal(cursor{s.scope, after})
		page.Cursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}

func (s *Store) Close() error {
	s.closed.Store(true)
	s.once.Do(func() { s.closeErr = s.backend.Close() })
	return s.closeErr
}
