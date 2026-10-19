// Package yuruidb provides JSON-typed, collection-scoped record storage.
// Import a driver to register it, then call Open[T], or use its constructor.
package yuruidb

import (
	"context"
	"encoding/json"
	"fmt"
)

type Page[T any] struct {
	Records []Record[T]
	Cursor  string
}

// Store binds a JSON data type to one collection. T must support encoding/json
// marshaling and unmarshaling. Unsupported values fail at runtime, before a
// mutation. Data uses normal encoding/json rules, including struct tags and
// custom marshalers. Use the same compatible schema when reopening a collection.
//
// List traverses immutable IDs in ascending byte order, without page snapshots.
// Update and Delete require a previously retrieved, nonempty Revision().
type Store[T any] struct {
	raw RawStore
}

// Open selects a statically imported driver and binds its collection to T.
// Options may be the driver's Config or a driver-neutral Config map.
func Open[T any](ctx context.Context, name string, options any) (*Store[T], error) {
	raw, err := OpenRaw(ctx, name, options)
	if err != nil {
		return nil, err
	}
	return Wrap[T](raw), nil
}

// Wrap adds JSON typing to a nonnil raw driver or adapter. The returned store
// owns the raw store's lifecycle; Close forwards to it.
func Wrap[T any](raw RawStore) *Store[T] { return &Store[T]{raw: raw} }

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
func (s *Store[T]) Insert(ctx context.Context, id string, data T) (Record[T], error) {
	b, snapshot, err := prepare(ctx, data)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := s.raw.Insert(ctx, id, b)
	if err != nil {
		return Record[T]{}, err
	}
	return typedRecord(raw, snapshot), nil
}

func (s *Store[T]) Get(ctx context.Context, id string) (Record[T], error) {
	raw, err := s.raw.Get(ctx, id)
	if err != nil {
		return Record[T]{}, err
	}
	return decode[T](raw)
}

func (s *Store[T]) List(ctx context.Context, options ListOptions) (Page[T], error) {
	raw, err := s.raw.List(ctx, options)
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
func (s *Store[T]) Update(ctx context.Context, id string, data T, revision string) (Record[T], error) {
	b, snapshot, err := prepare(ctx, data)
	if err != nil {
		return Record[T]{}, err
	}
	raw, err := s.raw.Update(ctx, id, b, revision)
	if err != nil {
		return Record[T]{}, err
	}
	return typedRecord(raw, snapshot), nil
}

func (s *Store[T]) Delete(ctx context.Context, id, revision string) error {
	return s.raw.Delete(ctx, id, revision)
}

func (s *Store[T]) Capabilities() Capabilities { return s.raw.Capabilities() }
func (s *Store[T]) Close() error               { return s.raw.Close() }

// Set unconditionally creates or replaces data on drivers that explicitly
// support this operation, currently Cloudflare KV. Revision cannot guard it.
// Other drivers return ErrUnsupported; this is never a fallback for Update.
func (s *Store[T]) Set(ctx context.Context, id string, data T) (Record[T], error) {
	setter, ok := s.raw.(interface {
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
func (s *Store[T]) Remove(ctx context.Context, id string) error {
	remover, ok := s.raw.(interface {
		Remove(context.Context, string) error
	})
	if !ok {
		return ErrUnsupported
	}
	return remover.Remove(ctx, id)
}
