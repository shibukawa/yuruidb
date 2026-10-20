package bbolt_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/bbolt"
	"github.com/shibukawa/yuruidb/internal/conformance"
	bolt "go.etcd.io/bbolt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		s, err := bbolt.OpenRaw(context.Background(), bbolt.Config{Path: filepath.Join(t.TempDir(), "db"), Collection: "items"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s, s
	})
}

func TestLiveBackupRestoresRecordsAndBlobs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, err := bbolt.Open(ctx, bbolt.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind[int]("counts"); err != nil {
		t.Fatal(err)
	}
	record, err := s.Insert(ctx, "job", "saved")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "count", 42); err != nil {
		t.Fatal(err)
	}
	blobs, err := bbolt.OpenBlobs(ctx, bbolt.Config{Path: path, Collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	meta, err := blobs.Insert(ctx, "document", strings.NewReader("document bytes"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	if err := s.Backup(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, "job", "changed", record.Revision()); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if err := os.WriteFile(restoredPath, snapshot.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := bbolt.Open(ctx, bbolt.Config{Path: restoredPath})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	if err := restored.Bind[int]("counts"); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Get[string](ctx, "job")
	if err != nil || got.Data != "saved" || got.Revision() == "" {
		t.Fatalf("restored record: %+v %v", got, err)
	}
	count, err := restored.Get[int](ctx, "count")
	if err != nil || count.Data != 42 {
		t.Fatalf("restored collection: %+v %v", count, err)
	}
	restoredBlobs, err := bbolt.OpenBlobs(ctx, bbolt.Config{Path: restoredPath, Collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	defer restoredBlobs.Close()
	r, err := restoredBlobs.Open(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != "document bytes" || r.Revision != meta.Revision {
		t.Fatalf("restored blob: %s %v", data, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Backup(cancelCtx, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, nil); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, failedWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("writer error: %v", err)
	}
	writeCtx, stop := context.WithCancel(ctx)
	if err := s.Backup(writeCtx, cancelWriter{stop}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled during backup: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Closing the record store leaves the separately owned Blob connection alive.
	if _, err := blobs.List(ctx, yuruidb.ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, io.Discard); !errors.Is(err, yuruidb.ErrClosed) {
		t.Fatal(err)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

func TestBlobInsertFailureRollsBack(t *testing.T) {
	s, err := bbolt.OpenBlobs(context.Background(), bbolt.Config{Path: filepath.Join(t.TempDir(), "db"), Collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A non-EOF source error must abort the transaction, leaving the ID reusable.
	if _, err := s.Insert(context.Background(), "job", io.MultiReader(strings.NewReader(strings.Repeat("partial", 20000)), errorInput{})); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), "job"); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Insert(context.Background(), "job", strings.NewReader("complete")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(context.Background(), "truncated", truncatedInput{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("source truncation was swallowed: %v", err)
	}
	if _, err := s.Open(context.Background(), "truncated"); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
}

type errorInput struct{}

func (errorInput) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

type truncatedInput struct{}

func (truncatedInput) Read(p []byte) (int, error) { return copy(p, "partial"), io.ErrUnexpectedEOF }
func TestPersistenceAndCollections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	config := bbolt.Config{Path: path, Collection: "items"}
	s, err := bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	id := conformance.ID(t)
	r, err := s.Insert(ctx, id, []byte("saved"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, id)
	if err != nil || got.Revision != r.Revision || string(got.Value) != "saved" {
		t.Fatalf("reopen: %v %v", got, err)
	}
	if err = s.Delete(ctx, id, got.Revision); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatalf("deleted record returned after reopen: %v", err)
	}
	s.Close()
	config.Collection = "other"
	s, err = bbolt.OpenRaw(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Get(ctx, id); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
	if p, err := s.List(ctx, yuruidb.ListOptions{}); err != nil || len(p.Records) != 0 {
		t.Fatalf("collection: %v %v", p, err)
	}
}

func TestBinarySize(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	id := "document"
	s, err := bbolt.OpenRaw(ctx, bbolt.Config{Path: path, Collection: "items"})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), 1<<20)
	if _, err = s.Insert(ctx, id, payload); err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.View(func(tx *bolt.Tx) error {
		stored := tx.Bucket([]byte("items")).Get([]byte("items/" + id))
		if len(stored) > len(payload)+128 {
			t.Fatalf("encoded bytes=%d payload=%d", len(stored), len(payload))
		}
		if !bytes.HasPrefix(stored, []byte("YRE1")) {
			t.Fatal("value is not binary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
