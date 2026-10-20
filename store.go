// Package yuruidb provides JSON-typed, multi-collection record storage.
// Import a driver to register it, call Open, and Bind each data type.
package yuruidb

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
)

type Page[T any] struct {
	Records []Record[T]
	Cursor  string
}

// Store owns a backend connection and binds distinct Go types to collections.
// Its generic methods encode JSON; Record[T] retains the requested data type.
// List uses ascending ID byte order and does not provide page snapshots.
type Store struct {
	raw         RawStore
	mu          sync.RWMutex
	bindings    map[reflect.Type]string
	collections map[string]RawStore
	closed      bool
	closeErr    error
}

// Open opens one backend. Omit Collection in driver options and call Bind for
// each data type. Driver constructors expose the same connection-scoped API.
func Open(ctx context.Context, name string, options any) (*Store, error) {
	raw, err := OpenRaw(ctx, name, options)
	if err != nil {
		return nil, err
	}
	return Wrap(raw), nil
}

// Wrap gives ownership of a raw backend to a JSON store. Bind each type before
// use. A third-party raw backend without OpenCollection can bind one collection.
func Wrap(raw RawStore) *Store {
	return &Store{raw: raw, bindings: make(map[reflect.Type]string), collections: make(map[string]RawStore)}
}

func dataType[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer && t.Name() == "" {
		t = t.Elem()
	}
	return t
}

// Bind registers a stable collection name for a data type. Defined types such
// as type Done Job and type Waiting Job are distinct; aliases are not. Pointer
// and value forms resolve to the same collection. Registration does no I/O.
func (s *Store) Bind[T any](collection string) error {
	if err := ValidateCollection(collection); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	t := dataType[T]()
	if old, ok := s.bindings[t]; ok {
		if old == collection {
			return nil
		}
		return fmt.Errorf("%w: type already bound to %q", ErrInvalid, old)
	}
	for other, name := range s.bindings {
		if name == collection && other != t {
			return fmt.Errorf("%w: collection already bound to another type", ErrInvalid)
		}
	}
	s.bindings[t] = collection
	return nil
}

func (s *Store) collection[T any](ctx context.Context) (RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	name, ok := s.bindings[dataType[T]()]
	if !ok {
		return nil, fmt.Errorf("%w: data type is not bound", ErrInvalid)
	}
	if raw := s.collections[name]; raw != nil {
		return raw, nil
	}
	var raw RawStore
	if opener, ok := s.raw.(CollectionOpener); ok {
		var err error
		raw, err = opener.OpenCollection(ctx, name)
		if err != nil {
			return nil, err
		}
	} else {
		if len(s.collections) != 0 {
			return nil, ErrUnsupported
		}
		raw = s.raw
	}
	s.collections[name] = raw
	return raw, nil
}

// prepare validates both directions before writing and creates an independent
// data snapshot. This also catches custom marshalers whose output T cannot read.
func prepare[T any](ctx context.Context, data T) ([]byte, T, error) {
	var snapshot T
	if err := ctx.Err(); err != nil {
		return nil, snapshot, err
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, snapshot, fmt.Errorf("%w: encode data: %w", ErrInvalid, err)
	}
	if err := json.Unmarshal(b, &snapshot); err != nil {
		return nil, snapshot, fmt.Errorf("%w: decode encoded data: %w", ErrInvalid, err)
	}
	return b, snapshot, nil
}

func decode[T any](raw RawRecord) (Record[T], error) {
	var data T
	if err := json.Unmarshal(raw.Value, &data); err != nil {
		return Record[T]{}, fmt.Errorf("yuruidb: decode record %q: %w", raw.ID, err)
	}
	return typedRecord(raw, data), nil
}

func typedRecord[T any](raw RawRecord, data T) Record[T] {
	return Record[T]{id: raw.ID, revision: raw.Revision, Data: data}
}

// Insert creates an absent ID. It never silently replaces an existing record.
func (s *Store) Insert[T any](ctx context.Context, id string, data T) (Record[T], error) {
	b, snapshot, err := prepare(ctx, data)
	if err != nil {
		return Record[T]{}, err
	}
	collection, err := s.collection[T](ctx)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := collection.Insert(ctx, id, b)
	if err != nil {
		return Record[T]{}, err
	}
	return typedRecord(raw, snapshot), nil
}

func (s *Store) Get[T any](ctx context.Context, id string) (Record[T], error) {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := collection.Get(ctx, id)
	if err != nil {
		return Record[T]{}, err
	}
	return decode[T](raw)
}

func (s *Store) List[T any](ctx context.Context, options ListOptions) (Page[T], error) {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return Page[T]{}, err
	}
	raw, err := collection.List(ctx, options)
	if err != nil {
		return Page[T]{}, err
	}
	page := Page[T]{Records: make([]Record[T], 0, len(raw.Records)), Cursor: raw.Cursor}
	for _, item := range raw.Records {
		record, err := decode[T](item)
		if err != nil {
			return Page[T]{}, err
		}
		page.Records = append(page.Records, record)
	}
	return page, nil
}

// Update replaces Data while preserving ID() and its position in List.
func (s *Store) Update[T any](ctx context.Context, id string, data T, revision string) (Record[T], error) {
	b, snapshot, err := prepare(ctx, data)
	if err != nil {
		return Record[T]{}, err
	}
	collection, err := s.collection[T](ctx)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := collection.Update(ctx, id, b, revision)
	if err != nil {
		return Record[T]{}, err
	}
	return typedRecord(raw, snapshot), nil
}

func (s *Store) Delete[T any](ctx context.Context, id, revision string) error {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return err
	}
	return collection.Delete(ctx, id, revision)
}

func (s *Store) Capabilities() Capabilities { return s.raw.Capabilities() }
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.closeErr = s.raw.Close()
	}
	return s.closeErr
}

// Set unconditionally creates or replaces data on drivers that explicitly
// support this operation, currently Cloudflare KV. Revision cannot guard it.
// Other drivers return ErrUnsupported; this is never a fallback for Update.
func (s *Store) Set[T any](ctx context.Context, id string, data T) (Record[T], error) {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return Record[T]{}, err
	}
	setter, ok := collection.(interface {
		Set(context.Context, string, []byte) (RawRecord, error)
	})
	if !ok {
		return Record[T]{}, ErrUnsupported
	}
	b, snapshot, err := prepare(ctx, data)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := setter.Set(ctx, id, b)
	if err != nil {
		return Record[T]{}, err
	}
	return typedRecord(raw, snapshot), nil
}

// Remove unconditionally deletes an ID on drivers with explicit support for
// this operation, currently Cloudflare KV. Other drivers return ErrUnsupported.
func (s *Store) Remove[T any](ctx context.Context, id string) error {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return err
	}
	remover, ok := collection.(interface {
		Remove(context.Context, string) error
	})
	if !ok {
		return ErrUnsupported
	}
	return remover.Remove(ctx, id)
}

// PurgeTombstones removes retained tombstones using conditional deletion.
func (s *Store) PurgeTombstones[T any](ctx context.Context) (int, error) {
	collection, err := s.collection[T](ctx)
	if err != nil {
		return 0, err
	}
	cleaner, ok := collection.(interface {
		PurgeTombstones(context.Context) (int, error)
	})
	if !ok {
		return 0, ErrUnsupported
	}
	return cleaner.PurgeTombstones(ctx)
}
