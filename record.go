package yuruidb

import "encoding/json"

// Record holds JSON data and read-only storage metadata. Modify Data and pass
// it to Update with ID() and Revision(); the returned record has a new revision.
type Record[T any] struct {
	id       string
	revision string
	Data     T
}

// ID returns the immutable record ID.
func (r Record[T]) ID() string { return r.id }

// Revision returns the opaque concurrency token for this retrieved version.
// Updating storage returns a new Record without modifying the original value.
func (r Record[T]) Revision() string { return r.revision }

type recordJSON[T any] struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Data     T      `json:"data"`
}

// MarshalJSON includes metadata as id and revision alongside data.
func (r Record[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordJSON[T]{ID: r.id, Revision: r.revision, Data: r.Data})
}

// UnmarshalJSON replaces the complete record, restoring metadata from JSON.
// A decoding error leaves the existing record unchanged.
func (r *Record[T]) UnmarshalJSON(data []byte) error {
	var wire recordJSON[T]
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*r = Record[T]{id: wire.ID, revision: wire.Revision, Data: wire.Data}
	return nil
}
