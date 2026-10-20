package bbolt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shibukawa/yuruidb"
	bolt "go.etcd.io/bbolt"
)

type pausedIO struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPausedIO(t *testing.T) *pausedIO {
	p := &pausedIO{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(p.resume)
	return p
}
func (p *pausedIO) resume() { p.once.Do(func() { close(p.release) }) }
func (p *pausedIO) wait(t *testing.T) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("I/O did not reach pause")
	}
}

type pausedWriter struct {
	*pausedIO
	first sync.Once
}

func (w *pausedWriter) Write(p []byte) (int, error) {
	w.first.Do(func() { close(w.entered); <-w.release })
	return len(p), nil
}

type pausedReader struct {
	*pausedIO
	first  sync.Once
	source io.Reader
}

func (r *pausedReader) Read(p []byte) (int, error) {
	r.first.Do(func() { close(r.entered); <-r.release })
	return r.source.Read(p)
}

func TestBackupAllowsRecordOperationsDuringOutput(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "job", "saved"); err != nil {
		t.Fatal(err)
	}
	pause := newPausedIO(t)
	backupDone := make(chan error, 1)
	go func() { backupDone <- s.Backup(ctx, &pausedWriter{pausedIO: pause}) }()
	pause.wait(t)
	opsDone := make(chan error, 1)
	go func() {
		got, err := s.Get[string](ctx, "job")
		if err == nil && got.Data != "saved" {
			err = errors.New("wrong record")
		}
		if err == nil {
			_, err = s.List[string](ctx, yuruidb.ListOptions{})
		}
		if err == nil {
			err = s.Bind[int]("counts")
		}
		opsDone <- err
	}()
	select {
	case err := <-opsDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		pause.resume()
		<-backupDone
		t.Fatal("record operations waited for the backup writer")
	}
	pause.resume()
	if err := <-backupDone; err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForBackupTransaction(t *testing.T) {
	s, err := Open(context.Background(), Config{Path: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pause := newPausedIO(t)
	backupDone := make(chan error, 1)
	go func() { backupDone <- s.Backup(context.Background(), &pausedWriter{pausedIO: pause}) }()
	pause.wait(t)
	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before backup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	pause.resume()
	if err := <-backupDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestBlobStagingAllowsUpdatesAndPublishesAtomically(t *testing.T) {
	for _, outcome := range []string{"success", "canceled", "source-error", "duplicate"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "db")
			s, err := Open(context.Background(), Config{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if err := s.Bind[string]("jobs"); err != nil {
				t.Fatal(err)
			}
			record, err := s.Insert(context.Background(), "job", "before")
			if err != nil {
				t.Fatal(err)
			}
			blobs, err := OpenBlobs(context.Background(), Config{Path: path, Collection: "documents"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { blobs.Close() })
			pause := newPausedIO(t)
			prefix := bytes.Repeat([]byte{0, 1, 2, 3, 255}, blobBatchSize*2/5+1)
			var tail io.Reader = strings.NewReader("tail")
			if outcome == "source-error" {
				tail = failingStageInput{}
			}
			done := make(chan error, 1)
			go func() {
				_, err := blobs.Insert(ctx, "document", io.MultiReader(bytes.NewReader(prefix), &pausedReader{pausedIO: pause, source: tail}))
				done <- err
			}()
			pause.wait(t)
			if page, err := blobs.List(context.Background(), yuruidb.ListOptions{}); err != nil || len(page.Blobs) != 0 {
				t.Fatalf("partial upload visible: %+v %v", page, err)
			}
			if _, err := blobs.Open(context.Background(), "document"); !errors.Is(err, yuruidb.ErrNotFound) {
				t.Fatalf("partial upload readable: %v", err)
			}
			opsDone := make(chan error, 1)
			go func() {
				_, err := s.Update(context.Background(), "job", "after", record.Revision())
				// A second owner must not recover another owner's live upload.
				if err == nil {
					peer, e := OpenBlobs(context.Background(), Config{Path: path, Collection: "documents"})
					err = e
					if e == nil {
						err = peer.Close()
					}
				}
				opsDone <- err
			}()
			select {
			case err := <-opsDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				pause.resume()
				<-done
				t.Fatal("record Update waited for Blob input")
			}
			if outcome == "canceled" {
				cancel()
			}
			if outcome == "duplicate" {
				if _, err := blobs.Insert(context.Background(), "document", strings.NewReader("winner")); err != nil {
					t.Fatal(err)
				}
			}
			pause.resume()
			err = <-done
			switch outcome {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
				r, e := blobs.Open(context.Background(), "document")
				if e != nil {
					t.Fatal(e)
				}
				data, e := io.ReadAll(r)
				r.Close()
				if e != nil || !bytes.Equal(data, append(prefix, []byte("tail")...)) {
					t.Fatalf("published data differs: %v", e)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "source-error":
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatal(err)
				}
			case "duplicate":
				if !errors.Is(err, yuruidb.ErrAlreadyExists) {
					t.Fatal(err)
				}
				r, e := blobs.Open(context.Background(), "document")
				if e != nil {
					t.Fatal(e)
				}
				data, e := io.ReadAll(r)
				r.Close()
				if e != nil || string(data) != "winner" {
					t.Fatalf("duplicate overwrote winner: %s %v", data, e)
				}
			}
			if outcome == "canceled" || outcome == "source-error" {
				if _, err := blobs.Insert(context.Background(), "document", strings.NewReader("retry")); err != nil {
					t.Fatal(err)
				}
			}
			// All owners must close before inspecting or reopening the file.
			blobs.Close()
			s.Close()
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.View(func(tx *bolt.Tx) error {
				return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
					if bytes.HasPrefix(name, []byte(blobStagingPrefix)) {
						return errors.New("upload staging was not cleaned")
					}
					return nil
				})
			})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failingStageInput struct{}

func (failingStageInput) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAbandonedBlobStagingRecoveredOnFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte(blobStagingPrefix + "documents"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("abandoned"), bytes.Repeat([]byte("x"), 100000))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err = bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(blobStagingPrefix+"documents")) != nil {
			return errors.New("abandoned staging remains")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBlobStagingBatchBoundaries(t *testing.T) {
	ctx := context.Background()
	blobs, err := OpenBlobs(ctx, Config{Path: filepath.Join(t.TempDir(), "db"), Collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	for _, size := range []int{0, 1, blobChunkSize - 1, blobChunkSize, blobBatchSize, blobBatchSize + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i * 31)
			}
			meta, err := blobs.Insert(ctx, strconv.Itoa(size), bytes.NewReader(payload))
			if err != nil || meta.Size != int64(size) {
				t.Fatalf("insert: %+v %v", meta, err)
			}
			r, err := blobs.Open(ctx, meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("batch boundary data differs: %v", err)
			}
		})
	}
}
