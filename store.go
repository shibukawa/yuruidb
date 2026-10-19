// Package yuruidb provides portable, collection-scoped record storage.
// Import a driver to register it, or use its constructor directly.
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

// Record contains caller-owned bytes. ID is immutable. Revision is an opaque
// concurrency token, not a timestamp or ordering key.
type Record struct {
	ID       string
	Value    []byte
	Revision string
}
type ListOptions struct {
	Limit  int
	Cursor string
}
type Page struct {
	Records []Record
	Cursor  string
}

// Capabilities describes guarantees, independently of driver availability.
type Capabilities struct {
	ConditionalMutations bool
	Distributed          bool
	StrongReads          bool
}

// Store is bound to one collection. List traverses immutable IDs in ascending
// byte order. Concurrent changes are not a snapshot across pages. Update and
// Delete require a nonempty revision previously returned by the store.
type Store interface {
	Insert(context.Context, string, []byte) (Record, error)
	Get(context.Context, string) (Record, error)
	List(context.Context, ListOptions) (Page, error)
	Update(context.Context, string, []byte, string) (Record, error)
	Delete(context.Context, string, string) error
	Capabilities() Capabilities
	Close() error
}
type Factory func(context.Context, any) (Store, error)

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

// Open selects a statically imported driver. Options can be that driver's
// Config value or a driver-neutral yuruidb.Config map. No dynamic loading or
// implicit provider detection occurs.
func Open(ctx context.Context, name string, options any) (Store, error) {
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
