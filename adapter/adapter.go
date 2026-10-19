// Package adapter reuses yuruidb's record, cursor and revision semantics for
// statically imported third-party KVS or object-storage drivers.
package adapter

import (
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
)

// Item is the raw encoded record and the backend's opaque concurrency token.
// The token must identify the stored incarnation, not merely the caller's ID.
type Item = recordstore.Item

// Backend is an atomic key store. Keys returns strictly ascending keys after
// the supplied key, scoped to the Config.Collection prefix. A blank expected
// token to Write means create-if-absent. A nonblank token means atomic CAS.
// A failed condition must return yuruidb.ErrConflict. Read returns
// yuruidb.ErrNotFound for absent keys. Keys may return fewer than limit keys,
// but must make progress whenever more is true.
//
// Keys passed to the backend are Collection + "/" + canonical UUIDv7 ID.
// Backends must preserve the opaque data supplied by Write. The adapter uses
// revision-bearing tombstones, including for logical deletion and recreation.
type Backend = recordstore.Transport

type Config struct {
	Collection string
	// Identity is a stable, nonsecret identifier for the backing data source;
	// it binds cursors to this driver/configuration across independent clients.
	Identity     string
	Capabilities yuruidb.Capabilities
}

func New(backend Backend, config Config) (yuruidb.Store, error) {
	if backend == nil || config.Identity == "" {
		return nil, yuruidb.ErrInvalid
	}
	return recordstore.New(backend, config.Collection, config.Identity, config.Capabilities)
}
