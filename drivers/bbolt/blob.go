package bbolt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/blob"
	bolt "go.etcd.io/bbolt"
)

const blobChunkSize = 64 << 10
const blobBatchSize = 2 << 20
const blobStagingPrefix = ".yuruidb-blob-staging/"

var blobMetaKey = []byte(".meta")

type blobBackend struct {
	t      *transport
	bucket []byte
	closed atomic.Bool
}

// OpenBlobs shares the reference-counted file connection with record stores.
// Documents live in reserved buckets, separately from record collections, and
// are stored in 64 KiB chunks. Insert reads input outside transactions, writes
// staging data in batches of at most 2 MiB, and atomically publishes the bucket.
func OpenBlobs(ctx context.Context, c Config) (*blob.Store, error) {
	if err := yuruidb.ValidateCollection(c.Collection); err != nil {
		return nil, err
	}
	collection := c.Collection
	c.Collection = ""
	t, err := openTransport(ctx, c)
	if err != nil {
		return nil, err
	}
	b := &blobBackend{t: t, bucket: []byte(".yuruidb-blobs/" + collection)}
	err = t.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(b.bucket)
		return err
	})
	if err != nil {
		t.Close()
		return nil, mapError(err)
	}
	return blob.NewBackend(b, "bbolt:"+t.path+"/blobs/"+collection)
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
	return b.t.Close()
}
func (b *blobBackend) metadata(tx *bolt.Tx, id string) (blob.Metadata, *bolt.Bucket, error) {
	bucket := tx.Bucket(b.bucket).Bucket([]byte(id))
	if bucket == nil {
		return blob.Metadata{}, nil, yuruidb.ErrNotFound
	}
	data := bucket.Get(blobMetaKey)
	if len(data) != 72 {
		return blob.Metadata{}, nil, fmt.Errorf("yuruidb: corrupt bbolt blob metadata")
	}
	size := int64(binary.BigEndian.Uint64(data[:8]))
	if size < 0 {
		return blob.Metadata{}, nil, fmt.Errorf("yuruidb: corrupt bbolt blob size")
	}
	return blob.Metadata{ID: id, Revision: string(data[8:]), Size: size}, bucket, nil
}

type blobInput struct {
	ctx    context.Context
	source io.Reader
	err    error
}

func (r *blobInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	r.err = err
	return n, err
}

func (b *blobBackend) stagingBucket() []byte {
	return []byte(blobStagingPrefix + string(b.bucket[len(".yuruidb-blobs/"):]))
}

// Only called when opening a file with no existing owners. Running this on a
// borrowed connection would remove uploads that are still receiving input.
func cleanupBlobStaging(db *bolt.DB) error {
	var buckets [][]byte
	err := db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			if bytes.HasPrefix(name, []byte(blobStagingPrefix)) {
				buckets = append(buckets, bytes.Clone(name))
			}
			return nil
		})
	})
	if err != nil || len(buckets) == 0 {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
		}
		return nil
	})
}

func (b *blobBackend) discardStaging(tx *bolt.Tx, revision string) error {
	root := tx.Bucket(b.stagingBucket())
	if root == nil {
		return nil
	}
	if root.Bucket([]byte(revision)) != nil {
		if err := root.DeleteBucket([]byte(revision)); err != nil {
			return err
		}
	}
	if key, _ := root.Cursor().First(); key == nil {
		return tx.DeleteBucket(b.stagingBucket())
	}
	return nil
}

func (b *blobBackend) Insert(ctx context.Context, id string, source io.Reader) (out blob.Metadata, result error) {
	if err := b.check(ctx); err != nil {
		return blob.Metadata{}, err
	}
	revision, err := yuruidb.NewRevision()
	if err != nil {
		return blob.Metadata{}, err
	}
	meta := blob.Metadata{ID: id, Revision: revision}
	err = b.t.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(b.bucket).Bucket([]byte(id)) != nil {
			return yuruidb.ErrAlreadyExists
		}
		return nil
	})
	if err != nil {
		return blob.Metadata{}, mapError(err)
	}
	staged := false
	defer func() {
		if staged {
			// Cleanup deliberately does not inherit a canceled upload context.
			if err := b.t.db.Update(func(tx *bolt.Tx) error { return b.discardStaging(tx, revision) }); err != nil {
				result = errors.Join(result, fmt.Errorf("yuruidb: cleaning Blob upload: %w", mapError(err)))
			}
		}
	}()
	buf := make([]byte, blobBatchSize)
	input := &blobInput{ctx: ctx, source: source}
	for {
		n, readErr := io.ReadFull(input, buf)
		if input.err != nil && input.err != io.EOF {
			return blob.Metadata{}, input.err
		}
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return blob.Metadata{}, readErr
		}
		final := readErr != nil || input.err == io.EOF
		err = b.t.db.Update(func(tx *bolt.Tx) error {
			if err := b.check(ctx); err != nil {
				return err
			}
			root, err := tx.CreateBucketIfNotExists(b.stagingBucket())
			if err != nil {
				return err
			}
			attempt := root.Bucket([]byte(revision))
			if !staged {
				attempt, err = root.CreateBucket([]byte(revision))
				if err != nil {
					return err
				}
				staged = true
				if _, err := attempt.CreateBucket([]byte(id)); err != nil {
					return err
				}
			}
			if attempt == nil {
				return fmt.Errorf("yuruidb: missing Blob upload staging")
			}
			bucket := attempt.Bucket([]byte(id))
			for start := 0; start < n; start += blobChunkSize {
				key := binary.BigEndian.AppendUint64(nil, uint64((meta.Size+int64(start))/blobChunkSize))
				if err := bucket.Put(key, buf[start:min(start+blobChunkSize, n)]); err != nil {
					return err
				}
			}
			if final {
				data := binary.BigEndian.AppendUint64(nil, uint64(meta.Size+int64(n)))
				return bucket.Put(blobMetaKey, append(data, revision...))
			}
			return nil
		})
		if err != nil {
			return blob.Metadata{}, mapError(err)
		}
		meta.Size += int64(n)
		if final {
			break
		}
	}
	// Move only an already committed bucket: changing its contents in this same
	// transaction would leave dirty child nodes behind in the source bucket.
	err = b.t.db.Update(func(tx *bolt.Tx) error {
		if err := b.check(ctx); err != nil {
			return err
		}
		root := tx.Bucket(b.bucket)
		if root.Bucket([]byte(id)) != nil {
			return yuruidb.ErrAlreadyExists
		}
		attempt := tx.Bucket(b.stagingBucket()).Bucket([]byte(revision))
		if err := attempt.MoveBucket([]byte(id), root); err != nil {
			return err
		}
		return b.discardStaging(tx, revision)
	})
	if err != nil {
		return blob.Metadata{}, mapError(err)
	}
	staged = false
	return meta, nil
}

type blobReader struct {
	b      *blobBackend
	ctx    context.Context
	meta   blob.Metadata
	stream *io.SectionReader
	closed atomic.Bool
}

func (b *blobBackend) Open(ctx context.Context, id string) (*blob.Reader, error) {
	if err := b.check(ctx); err != nil {
		return nil, err
	}
	var meta blob.Metadata
	err := b.t.db.View(func(tx *bolt.Tx) error {
		var err error
		meta, _, err = b.metadata(tx, id)
		return err
	})
	if err != nil {
		return nil, mapError(err)
	}
	r := &blobReader{b: b, ctx: ctx, meta: meta}
	r.stream = io.NewSectionReader(r, 0, meta.Size)
	return &blob.Reader{ReadCloser: r, ReaderAt: r, Metadata: meta}, nil
}
func (r *blobReader) Close() error { r.closed.Store(true); return nil }
func (r *blobReader) Read(p []byte) (int, error) {
	if r.closed.Load() {
		return 0, yuruidb.ErrClosed
	}
	if err := r.b.check(r.ctx); err != nil {
		return 0, err
	}
	return r.stream.Read(p)
}
func (r *blobReader) ReadAt(p []byte, off int64) (int, error) {
	if r.closed.Load() {
		return 0, yuruidb.ErrClosed
	}
	if err := r.b.check(r.ctx); err != nil {
		return 0, err
	}
	if off < 0 {
		return 0, yuruidb.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	err := r.b.t.db.View(func(tx *bolt.Tx) error {
		meta, bucket, err := r.b.metadata(tx, r.meta.ID)
		if err != nil {
			return err
		}
		if meta.Revision != r.meta.Revision {
			return yuruidb.ErrConflict
		}
		for n < len(p) && off < meta.Size {
			if err := r.b.check(r.ctx); err != nil {
				return err
			}
			chunk := bucket.Get(binary.BigEndian.AppendUint64(nil, uint64(off/blobChunkSize)))
			start := int(off % blobChunkSize)
			want := min(int64(blobChunkSize), meta.Size-(off/blobChunkSize)*blobChunkSize)
			if int64(len(chunk)) != want || start >= len(chunk) {
				return fmt.Errorf("yuruidb: corrupt bbolt blob chunk")
			}
			copied := copy(p[n:], chunk[start:])
			n += copied
			off += int64(copied)
		}
		if n < len(p) {
			return io.EOF
		}
		return nil
	})
	return n, mapError(err)
}

func (b *blobBackend) Delete(ctx context.Context, id, revision string) error {
	return b.remove(ctx, id, revision)
}
func (b *blobBackend) Remove(ctx context.Context, id string) error { return b.remove(ctx, id, "") }
func (b *blobBackend) remove(ctx context.Context, id, revision string) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	return mapError(b.t.db.Update(func(tx *bolt.Tx) error {
		if err := b.check(ctx); err != nil {
			return err
		}
		meta, _, err := b.metadata(tx, id)
		if err != nil {
			return err
		}
		if revision != "" && revision != meta.Revision {
			return yuruidb.ErrConflict
		}
		return tx.Bucket(b.bucket).DeleteBucket([]byte(id))
	}))
}
func (b *blobBackend) List(ctx context.Context, after string, n int) ([]blob.Metadata, bool, error) {
	if err := b.check(ctx); err != nil {
		return nil, false, err
	}
	items := []blob.Metadata{}
	err := b.t.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(b.bucket).Cursor()
		k, _ := c.Seek([]byte(after))
		if bytes.Equal(k, []byte(after)) {
			k, _ = c.Next()
		}
		for ; k != nil && len(items) <= n; k, _ = c.Next() {
			if err := b.check(ctx); err != nil {
				return err
			}
			meta, _, err := b.metadata(tx, string(k))
			if err != nil {
				return err
			}
			items = append(items, meta)
		}
		return nil
	})
	return items[:min(n, len(items))], len(items) > n, mapError(err)
}
