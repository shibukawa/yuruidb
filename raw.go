package yuruidb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
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
// opaque bytes at this layer; Store handles JSON encoding. Each store is bound
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

// CollectionOpener supplies borrowed collection views. Their lifetime is owned
// by the root RawStore; closing a view must not close the shared connection.
type CollectionOpener interface {
	OpenCollection(context.Context, string) (RawStore, error)
}

// MutationError preserves the identity of an attempted mutation for later
// reconciliation. Nonce is stored in the envelope; AttemptedRevision may omit
// a native token when the response was lost. Unwrap preserves errors.Is.
type MutationError struct {
	Operation, Collection, ID, Nonce, ExpectedRevision, AttemptedRevision string
	Cause                                                                 error
}

func (e *MutationError) Error() string {
	return "yuruidb: " + e.Operation + " outcome unknown for " + e.Collection + "/" + e.ID
}
func (e *MutationError) Unwrap() error { return errors.Join(ErrOutcomeUnknown, e.Cause) }

// MatchesRevision reports whether a retrieved revision identifies this exact
// attempted write, ignoring a native token unavailable after response loss.
// False does not prove failure: the write may have been superseded or deleted.
func (e *MutationError) MatchesRevision(value string) bool {
	if e.Operation == "set" && e.Nonce != "" && e.AttemptedRevision == e.Nonce {
		return value == e.Nonce
	}
	decode := func(value string) (string, string, string, bool) {
		if !strings.HasPrefix(value, "r1.") {
			return "", "", "", false
		}
		b, err := base64.RawURLEncoding.DecodeString(value[3:])
		var r struct {
			Scope string `json:"s"`
			ID    string `json:"i"`
			Nonce string `json:"n"`
		}
		if err != nil || json.Unmarshal(b, &r) != nil || r.Nonce == "" {
			return "", "", "", false
		}
		return r.Scope, r.ID, r.Nonce, true
	}
	scope, id, nonce, ok := decode(value)
	expectedScope, expectedID, expectedNonce, valid := decode(e.AttemptedRevision)
	return ok && valid && scope == expectedScope && id == expectedID && nonce == expectedNonce
}
