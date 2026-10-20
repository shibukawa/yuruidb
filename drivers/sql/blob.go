package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/blob"
)

var blobTableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

type blobBackend struct {
	exec                       executor
	table, collection, dialect string
	closed                     atomic.Bool
}

// OpenBlobs uses the same Config and Blob API as the other document drivers.
// Table defaults to ururidb_blobs, separately from record storage. AutoMigrate
// creates the table; otherwise the caller supplies its schema. SQLite uses BLOB
// and PostgreSQL uses BYTEA. Insert buffers its input; reads retrieve only ranges.
// CloseDB follows the record driver's ownership rules for the injected DB.
func OpenBlobs(ctx context.Context, c Config) (*blob.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.DB == nil {
		return nil, fmt.Errorf("%w: SQL DB required", yuruidb.ErrInvalid)
	}
	if err := yuruidb.ValidateCollection(c.Collection); err != nil {
		return nil, err
	}
	if c.Table == "" {
		c.Table = "ururidb_blobs"
	}
	if !blobTableName.MatchString(c.Table) {
		return nil, fmt.Errorf("%w: invalid SQL blob table identifier", yuruidb.ErrInvalid)
	}
	if c.Dialect == "" {
		c.Dialect = "sqlite"
	}
	if c.Dialect != "sqlite" && c.Dialect != "postgres" {
		return nil, fmt.Errorf("%w: SQL dialect %q", yuruidb.ErrUnsupported, c.Dialect)
	}
	b := &blobBackend{exec: executor{c.DB, c.CloseDB}, table: `"` + c.Table + `"`, collection: c.Collection, dialect: c.Dialect}
	if c.AutoMigrate {
		binaryType, collation := "BLOB", "BINARY"
		if c.Dialect == "postgres" {
			binaryType, collation = "BYTEA", `"C"`
		}
		query := "CREATE TABLE IF NOT EXISTS " + b.table + " (collection TEXT NOT NULL, id TEXT COLLATE " + collation + " NOT NULL, payload " + binaryType + " NOT NULL, token TEXT NOT NULL, size BIGINT NOT NULL CHECK (size >= 0), PRIMARY KEY (collection, id))"
		if _, err := b.exec.Exec(ctx, query, nil); err != nil {
			return nil, err
		}
	}
	return blob.NewBackend(b, "sql:"+c.Identity+":"+c.Table+":"+c.Dialect+":"+c.Collection)
}

func (b *blobBackend) bind(query string) string {
	if b.dialect != "postgres" {
		return query
	}
	i := 0
	var out strings.Builder
	for _, r := range query {
		if r == '?' {
			i++
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(i))
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func (b *blobBackend) check(ctx context.Context) error {
	if b.closed.Load() {
		return yuruidb.ErrClosed
	}
	return ctx.Err()
}
func (b *blobBackend) Close() error {
	if b.closed.Swap(true) {
		return nil
	}
	return b.exec.Close()
}

type blobInput struct {
	ctx    context.Context
	source io.Reader
}

func (r blobInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func (b *blobBackend) Insert(ctx context.Context, id string, source io.Reader) (blob.Metadata, error) {
	if err := b.check(ctx); err != nil {
		return blob.Metadata{}, err
	}
	payload, err := io.ReadAll(blobInput{ctx, source})
	if err != nil {
		return blob.Metadata{}, err
	}
	token, err := yuruidb.NewRevision()
	if err != nil {
		return blob.Metadata{}, err
	}
	meta := blob.Metadata{ID: id, Revision: token, Size: int64(len(payload))}
	query := "INSERT INTO " + b.table + " (collection, id, payload, token, size) VALUES (?, ?, ?, ?, ?) ON CONFLICT (collection, id) DO NOTHING"
	n, err := b.exec.Exec(ctx, b.bind(query), []any{b.collection, id, payload, token, meta.Size})
	if err != nil {
		return blob.Metadata{}, err
	}
	if n == 0 {
		return blob.Metadata{}, yuruidb.ErrAlreadyExists
	}
	if n != 1 {
		return blob.Metadata{}, yuruidb.OutcomeUnknown(fmt.Errorf("yuruidb: unexpected SQL blob insert count"))
	}
	return meta, nil
}

func (b *blobBackend) Open(ctx context.Context, id string) (*blob.Reader, error) {
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	meta := blob.Metadata{ID: id}
	query := "SELECT token, size FROM " + b.table + " WHERE collection = ? AND id = ?"
	err := b.exec.db.QueryRowContext(ctx, b.bind(query), b.collection, id).Scan(&meta.Revision, &meta.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, yuruidb.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if meta.Revision == "" || meta.Size < 0 {
		return nil, fmt.Errorf("yuruidb: corrupt SQL blob metadata")
	}
	r := &blobReader{backend: b, ctx: ctx, meta: meta}
	r.stream = io.NewSectionReader(r, 0, meta.Size)
	return &blob.Reader{ReadCloser: r, ReaderAt: r, Metadata: meta}, nil
}

type blobReader struct {
	backend *blobBackend
	ctx     context.Context
	meta    blob.Metadata
	stream  *io.SectionReader
	closed  atomic.Bool
}

func (r *blobReader) check() error {
	if r.closed.Load() {
		return yuruidb.ErrClosed
	}
	return r.backend.check(r.ctx)
}
func (r *blobReader) Close() error { r.closed.Store(true); return nil }
func (r *blobReader) Read(p []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.stream.Read(p)
}
func (r *blobReader) ReadAt(p []byte, off int64) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, yuruidb.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.meta.Size {
		return 0, io.EOF
	}
	length := min(int64(len(p)), r.meta.Size-off)
	query := "SELECT token, size, substr(payload, ?, ?) FROM " + r.backend.table + " WHERE collection = ? AND id = ?"
	var token string
	var size int64
	var payload []byte
	err := r.backend.exec.db.QueryRowContext(r.ctx, r.backend.bind(query), off+1, length, r.backend.collection, r.meta.ID).Scan(&token, &size, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, yuruidb.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if token != r.meta.Revision {
		return 0, yuruidb.ErrConflict
	}
	if size != r.meta.Size || int64(len(payload)) != length {
		return 0, fmt.Errorf("yuruidb: corrupt SQL blob range")
	}
	n := copy(p, payload)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (b *blobBackend) Delete(ctx context.Context, id, revision string) error {
	return b.remove(ctx, id, revision)
}
func (b *blobBackend) Remove(ctx context.Context, id string) error { return b.remove(ctx, id, "") }
func (b *blobBackend) remove(ctx context.Context, id, revision string) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	query := "DELETE FROM " + b.table + " WHERE collection = ? AND id = ?"
	args := []any{b.collection, id}
	if revision != "" {
		query += " AND token = ?"
		args = append(args, revision)
	}
	n, err := b.exec.Exec(ctx, b.bind(query), args)
	if err != nil {
		return err
	}
	if n == 0 {
		if revision != "" {
			return yuruidb.ErrConflict
		}
		return yuruidb.ErrNotFound
	}
	if n != 1 {
		return yuruidb.OutcomeUnknown(fmt.Errorf("yuruidb: unexpected SQL blob delete count"))
	}
	return nil
}

func (b *blobBackend) List(ctx context.Context, after string, limit int) ([]blob.Metadata, bool, error) {
	if err := b.check(ctx); err != nil {
		return nil, false, err
	}
	query := "SELECT id, token, size FROM " + b.table + " WHERE collection = ? AND id > ? ORDER BY id ASC LIMIT ?"
	rows, err := b.exec.db.QueryContext(ctx, b.bind(query), b.collection, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []blob.Metadata{}
	for rows.Next() {
		var meta blob.Metadata
		if err := rows.Scan(&meta.ID, &meta.Revision, &meta.Size); err != nil {
			return nil, false, err
		}
		items = append(items, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return items[:min(limit, len(items))], len(items) > limit, nil
}
