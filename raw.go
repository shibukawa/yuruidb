package yuruidb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrNotFound      = errors.New("yuruidb: record not found")
	ErrAlreadyExists = errors.New("yuruidb: record already exists")
	ErrConflict      = errors.New("yuruidb: revision conflict")
	ErrUnsupported   = errors.New("yuruidb: unsupported capability")
	ErrInvalid       = errors.New("yuruidb: invalid argument")
	ErrClosed        = errors.New("yuruidb: store closed")
	// ErrOutcomeUnknown means a remote mutation may have committed. Do not retry
	// with a different ID or revision without reconciling the outcome.
	ErrOutcomeUnknown = errors.New("yuruidb: mutation outcome unknown")
)

// RawRecord is the byte-oriented driver record. Value contains caller-owned
// bytes. ID is immutable; Revision is an opaque concurrency token.
type RawRecord struct {
	ID       string
	Value    []byte
	Revision string
}
type ListOptions struct {
	Limit  int
	Cursor string
}
type RawPage struct {
	Records []RawRecord
	Cursor  string
}

// Capabilities describes guarantees, independently of driver availability.
type Capabilities struct {
	ConditionalMutations bool
	Distributed          bool
	StrongReads          bool
}

// RawStore is the byte-oriented contract implemented by drivers. Values are
// opaque bytes at this layer; Store[T] handles JSON encoding. Each store is bound
// to one collection. List traverses immutable IDs in ascending byte order, without
// page snapshots. Update and Delete require a previously retrieved revision.
type RawStore interface {
	Insert(context.Context, string, []byte) (RawRecord, error)
	Get(context.Context, string) (RawRecord, error)
	List(context.Context, ListOptions) (RawPage, error)
	Update(context.Context, string, []byte, string) (RawRecord, error)
	Delete(context.Context, string, string) error
	Capabilities() Capabilities
	Close() error
}
type Factory func(context.Context, any) (RawStore, error)

var registry = struct {
	sync.RWMutex
	factories map[string]Factory
}{factories: make(map[string]Factory)}

// Register is intended for driver init functions. Registration does no I/O.
func Register(name string, factory Factory) {
	if name == "" || factory == nil {
		panic("yuruidb: invalid driver registration")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.factories[name]; exists {
		panic("yuruidb: duplicate driver " + name)
	}
	registry.factories[name] = factory
}
func Drivers() []string {
	registry.RLock()
	defer registry.RUnlock()
	names := make([]string, 0, len(registry.factories))
	for name := range registry.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// OpenRaw selects a statically imported driver. Options can be that driver's
// Config value or a driver-neutral yuruidb.Config map. No dynamic loading or
// implicit provider detection occurs.
func OpenRaw(ctx context.Context, name string, options any) (RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registry.RLock()
	factory := registry.factories[name]
	registry.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("%w: driver %q is not imported", ErrUnsupported, name)
	}
	return factory(ctx, options)
}
