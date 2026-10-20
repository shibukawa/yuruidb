// Package recordstore implements record semantics on atomic conditional stores.
package recordstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/shibukawa/yuruidb"
)

type Item struct {
	Data  []byte
	Token string
}
type Entry struct {
	Key  string
	Item Item
}

// Empty expected token means create-if-absent. Nonempty means atomic CAS.
type Transport interface {
	Read(context.Context, string) (Item, error)
	Write(context.Context, string, []byte, string) error
	Keys(context.Context, string, int) ([]string, bool, error)
	Close() error
}

// Optional extensions preserve the original third-party transport contract.
type ItemWriter interface {
	WriteItem(context.Context, string, []byte, string) (string, error)
}
type Deleter interface {
	Delete(context.Context, string, string) error
}
type Scanner interface {
	Scan(context.Context, string, int) ([]Entry, bool, error)
}
type CollectionTransport interface {
	ForCollection(context.Context, string) (Transport, error)
}

type Envelope struct {
	ID       string `json:"id"`
	Value    []byte `json:"value,omitempty"`
	Revision string `json:"revision"`
	Deleted  bool   `json:"deleted,omitempty"`
}
type Store struct {
	closed                  atomic.Bool
	closeOnce               sync.Once
	closeErr                error
	transport               Transport
	prefix, scope, identity string
	caps                    yuruidb.Capabilities
	owner                   *Store
}

func New(t Transport, collection, identity string, caps yuruidb.Capabilities) (*Store, error) {
	if collection != "" {
		if err := yuruidb.ValidateCollection(collection); err != nil {
			return nil, err
		}
	}
	h := sha256.Sum256([]byte(identity + "/" + collection))
	prefix := ""
	if collection != "" {
		prefix = collection + "/"
	}
	return &Store{transport: t, prefix: prefix, scope: hex.EncodeToString(h[:]), identity: identity, caps: caps}, nil
}
func (s *Store) OpenCollection(ctx context.Context, name string) (yuruidb.RawStore, error) {
	if err := yuruidb.ValidateCollection(name); err != nil {
		return nil, err
	}
	if err := s.checkOwner(ctx); err != nil {
		return nil, err
	}
	t := s.transport
	if name+"/" != s.prefix {
		opener, ok := t.(CollectionTransport)
		if !ok {
			return nil, yuruidb.ErrUnsupported
		}
		var err error
		t, err = opener.ForCollection(ctx, name)
		if err != nil {
			return nil, err
		}
	}
	view, err := New(t, name, s.identity, s.caps)
	if err == nil {
		view.owner = s
	}
	return view, err
}
func (s *Store) checkOwner(ctx context.Context) error {
	if s.closed.Load() {
		return yuruidb.ErrClosed
	}
	if s.owner != nil {
		return s.owner.checkOwner(ctx)
	}
	return ctx.Err()
}
func (s *Store) Check(ctx context.Context) error {
	if err := s.checkOwner(ctx); err != nil {
		return err
	}
	if s.prefix == "" {
		return fmt.Errorf("%w: collection required", yuruidb.ErrInvalid)
	}
	return nil
}
func (s *Store) Capabilities() yuruidb.Capabilities { return s.caps }
func (s *Store) Close() error {
	s.closed.Store(true)
	s.closeOnce.Do(func() {
		if s.owner == nil {
			s.closeErr = s.transport.Close()
		}
	})
	return s.closeErr
}

// BinaryEnvelope stores the actual payload without base64 in local stores.
// Remote transports use JSON envelopes; local transports use this binary form.
func BinaryEnvelope(e Envelope) []byte {
	b := make([]byte, 7+len(e.ID)+len(e.Revision)+len(e.Value))
	copy(b, "YRE1")
	binary.BigEndian.PutUint16(b[4:6], uint16(len(e.ID)))
	if e.Deleted {
		b[6] = 1
	}
	n := 7 + copy(b[7:], e.ID)
	n += copy(b[n:], e.Revision)
	copy(b[n:], e.Value)
	return b
}
func Decode(data []byte, id string) (Envelope, error) {
	var e Envelope
	if bytes.HasPrefix(data, []byte("YRE1")) {
		if len(data) < 7 {
			return e, fmt.Errorf("yuruidb: corrupt binary envelope")
		}
		n := int(binary.BigEndian.Uint16(data[4:6]))
		if len(data) < 7+n+64 || data[6] > 1 {
			return e, fmt.Errorf("yuruidb: corrupt binary envelope")
		}
		e = Envelope{ID: string(data[7 : 7+n]), Revision: string(data[7+n : 7+n+64]), Deleted: data[6] == 1, Value: append([]byte(nil), data[7+n+64:]...)}
	} else if err := json.Unmarshal(data, &e); err != nil {
		return e, fmt.Errorf("yuruidb: corrupt record: %w", err)
	}
	if e.ID != id || e.Revision == "" {
		return e, fmt.Errorf("yuruidb: corrupt record identity or revision")
	}
	return e, nil
}

type revision struct {
	Scope string `json:"s"`
	ID    string `json:"i"`
	Nonce string `json:"n"`
	Token string `json:"t,omitempty"`
}

func (s *Store) revision(e Envelope, token string) string {
	if !s.caps.ConditionalMutations {
		return e.Revision
	}
	b, _ := json.Marshal(revision{s.scope, e.ID, e.Revision, token})
	return "r1." + base64.RawURLEncoding.EncodeToString(b)
}
func (s *Store) expected(id, value string) (revision, error) {
	if value == "" {
		return revision{}, fmt.Errorf("%w: expected revision required", yuruidb.ErrInvalid)
	}
	if !strings.HasPrefix(value, "r1.") {
		return revision{}, fmt.Errorf("%w: malformed revision", yuruidb.ErrInvalid)
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "r1."))
	var r revision
	if err != nil || json.Unmarshal(b, &r) != nil || r.Scope != s.scope || r.ID != id || r.Nonce == "" {
		return r, fmt.Errorf("%w: revision does not belong to this record", yuruidb.ErrInvalid)
	}
	return r, nil
}
func (s *Store) record(e Envelope, token string) yuruidb.RawRecord {
	return yuruidb.RawRecord{ID: e.ID, Value: append([]byte(nil), e.Value...), Revision: s.revision(e, token)}
}
func (s *Store) read(ctx context.Context, id string) (Envelope, string, error) {
	if err := s.Check(ctx); err != nil {
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
	e, token, err := s.read(ctx, id)
	if err == nil && e.Deleted {
		err = yuruidb.ErrNotFound
	}
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	return s.record(e, token), nil
}
func (s *Store) unknown(op string, e Envelope, expected string, cause error) error {
	return &yuruidb.MutationError{Operation: op, Collection: strings.TrimSuffix(s.prefix, "/"), ID: e.ID, Nonce: e.Revision, ExpectedRevision: expected, AttemptedRevision: s.revision(e, ""), Cause: cause}
}
func (s *Store) write(ctx context.Context, op string, e Envelope, token, expected string) (yuruidb.RawRecord, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	var next string
	if w, ok := s.transport.(ItemWriter); ok {
		next, err = w.WriteItem(ctx, s.prefix+e.ID, b, token)
	} else {
		err = s.transport.Write(ctx, s.prefix+e.ID, b, token)
	}
	if errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		if ctx.Err() == nil && s.caps.StrongReads {
			got, t, readErr := s.read(ctx, e.ID)
			if readErr == nil && got.Revision == e.Revision {
				return s.record(e, t), nil
			}
		}
		return yuruidb.RawRecord{}, s.unknown(op, e, expected, err)
	}
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	if next == "" && s.caps.ConditionalMutations {
		got, t, readErr := s.read(ctx, e.ID)
		if readErr == nil && got.Revision == e.Revision {
			next = t
		}
	}
	return s.record(e, next), nil
}
func (s *Store) Insert(ctx context.Context, id string, value []byte) (yuruidb.RawRecord, error) {
	if err := s.Check(ctx); err != nil {
		return yuruidb.RawRecord{}, err
	}
	if !s.caps.ConditionalMutations {
		return yuruidb.RawRecord{}, yuruidb.ErrUnsupported
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return yuruidb.RawRecord{}, err
	}
	rev, err := yuruidb.NewRevision()
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	e := Envelope{ID: id, Value: value, Revision: rev}
	r, err := s.write(ctx, "insert", e, "", "")
	if !errors.Is(err, yuruidb.ErrConflict) || errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		return r, err
	}
	old, token, readErr := s.read(ctx, id)
	if errors.Is(readErr, yuruidb.ErrNotFound) { // a concurrent physical deletion
		r, err = s.write(ctx, "insert", e, "", "")
	} else if readErr != nil {
		return yuruidb.RawRecord{}, readErr
	} else if !old.Deleted {
		return yuruidb.RawRecord{}, yuruidb.ErrAlreadyExists
	} else {
		r, err = s.write(ctx, "insert", e, token, "")
	}
	if errors.Is(err, yuruidb.ErrConflict) && !errors.Is(err, yuruidb.ErrOutcomeUnknown) {
		err = yuruidb.ErrAlreadyExists
	}
	return r, err
}
func (s *Store) mutationToken(ctx context.Context, id, expected string) (string, error) {
	if err := s.Check(ctx); err != nil {
		return "", err
	}
	if err := yuruidb.ValidateID(id); err != nil {
		return "", err
	}
	r, err := s.expected(id, expected)
	if err != nil {
		return "", err
	}
	if r.Token != "" {
		return r.Token, nil
	}
	old, token, err := s.read(ctx, id)
	if err != nil {
		return "", err
	}
	if old.Deleted {
		return "", yuruidb.ErrNotFound
	}
	if old.Revision != r.Nonce {
		return "", yuruidb.ErrConflict
	}
	if token == "" {
		return "", fmt.Errorf("yuruidb: backend omitted concurrency token")
	}
	return token, nil
}
func (s *Store) Update(ctx context.Context, id string, value []byte, expected string) (yuruidb.RawRecord, error) {
	if !s.caps.ConditionalMutations {
		return yuruidb.RawRecord{}, yuruidb.ErrUnsupported
	}
	token, err := s.mutationToken(ctx, id, expected)
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	rev, err := yuruidb.NewRevision()
	if err != nil {
		return yuruidb.RawRecord{}, err
	}
	return s.write(ctx, "update", Envelope{ID: id, Value: value, Revision: rev}, token, expected)
}
func (s *Store) Delete(ctx context.Context, id, expected string) error {
	if !s.caps.ConditionalMutations {
		return yuruidb.ErrUnsupported
	}
	token, err := s.mutationToken(ctx, id, expected)
	if err != nil {
		return err
	}
	if d, ok := s.transport.(Deleter); ok {
		err = d.Delete(ctx, s.prefix+id, token)
		if errors.Is(err, yuruidb.ErrOutcomeUnknown) {
			return s.unknown("delete", Envelope{ID: id}, expected, err)
		}
		if !errors.Is(err, yuruidb.ErrUnsupported) {
			return err
		}
	}
	rev, err := yuruidb.NewRevision()
	if err != nil {
		return err
	}
	_, err = s.write(ctx, "delete", Envelope{ID: id, Revision: rev, Deleted: true}, token, expected)
	return err
}

// PurgeTombstones conditionally removes retained tombstones. A concurrent
// recreation changes the token and is never deleted. Only capable stores work.
func (s *Store) PurgeTombstones(ctx context.Context) (int, error) {
	if err := s.Check(ctx); err != nil {
		return 0, err
	}
	d, ok := s.transport.(Deleter)
	if !ok {
		return 0, yuruidb.ErrUnsupported
	}
	count := 0
	after := ""
	for {
		keys, more, err := s.transport.Keys(ctx, after, 1000)
		if err != nil {
			return count, err
		}
		if len(keys) == 0 && more {
			return count, fmt.Errorf("yuruidb: non-progressing backend listing")
		}
		for _, key := range keys {
			if !strings.HasPrefix(key, s.prefix) || key <= after {
				return count, fmt.Errorf("yuruidb: invalid backend key ordering")
			}
			after = key
			e, token, err := s.read(ctx, strings.TrimPrefix(key, s.prefix))
			if errors.Is(err, yuruidb.ErrNotFound) {
				continue
			}
			if err != nil {
				return count, err
			}
			if e.Deleted {
				err = d.Delete(ctx, key, token)
				if err == nil {
					count++
				} else if !errors.Is(err, yuruidb.ErrConflict) && !errors.Is(err, yuruidb.ErrNotFound) {
					return count, err
				}
			}
		}
		if !more {
			return count, nil
		}
	}
}

type cursor struct {
	Scope string `json:"s"`
	After string `json:"a"`
}

func (s *Store) List(ctx context.Context, opts yuruidb.ListOptions) (yuruidb.RawPage, error) {
	if err := s.Check(ctx); err != nil {
		return yuruidb.RawPage{}, err
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
		if err := s.Check(ctx); err != nil {
			return yuruidb.RawPage{}, err
		}
		var entries []Entry
		var more bool
		var err error
		scanner, scan := s.transport.(Scanner)
		if scan {
			entries, more, err = scanner.Scan(ctx, after, limit+1)
		} else {
			var keys []string
			keys, more, err = s.transport.Keys(ctx, after, limit+1)
			for _, k := range keys {
				entries = append(entries, Entry{Key: k})
			}
		}
		if err != nil {
			return yuruidb.RawPage{}, err
		}
		if len(entries) == 0 {
			if more {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: non-progressing backend listing")
			}
			return page, nil
		}
		for i, entry := range entries {
			key := entry.Key
			if !strings.HasPrefix(key, s.prefix) || key <= after {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: invalid backend key ordering")
			}
			after = key
			id := strings.TrimPrefix(key, s.prefix)
			if err := yuruidb.ValidateID(id); err != nil {
				return yuruidb.RawPage{}, fmt.Errorf("yuruidb: foreign key in collection: %w", err)
			}
			var r yuruidb.RawRecord
			if scan {
				var e Envelope
				e, err = Decode(entry.Item.Data, id)
				if err == nil && e.Deleted {
					err = yuruidb.ErrNotFound
				}
				if err == nil {
					r = s.record(e, entry.Item.Token)
				}
			} else {
				r, err = s.Get(ctx, id)
			}
			if err != nil && !errors.Is(err, yuruidb.ErrNotFound) {
				return yuruidb.RawPage{}, err
			}
			if err == nil {
				page.Records = append(page.Records, r)
			}
			if len(page.Records) == limit {
				if more || i+1 < len(entries) {
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
