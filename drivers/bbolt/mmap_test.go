package bbolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shibukawa/yuruidb"
)

func TestInitialMmapSizeAllowsUpdateDuringBackup(t *testing.T) {
	ctx := context.Background()
	// Exercise the driver-neutral configuration path as well as the bbolt option.
	s, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{
		"Path": filepath.Join(t.TempDir(), "db"), "InitialMmapSize": 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	record, err := s.Insert(ctx, "job", "before")
	if err != nil {
		t.Fatal(err)
	}
	pause := newPausedIO(t)
	backupDone := make(chan error, 1)
	go func() { backupDone <- s.Backup(ctx, &pausedWriter{pausedIO: pause}) }()
	pause.wait(t)
	// This grows the file beyond bbolt's small default mapping, while remaining
	// comfortably within the explicitly reserved mapping. No remap is needed.
	payload := strings.Repeat("x", 1<<20)
	updateDone := make(chan error, 1)
	go func() { _, err := s.Update(ctx, "job", payload, record.Revision()); updateDone <- err }()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		pause.resume()
		<-backupDone
		<-updateDone
		t.Fatal("Update waited for backup despite sufficient InitialMmapSize")
	}
	got, err := s.Get[string](ctx, "job")
	if err != nil || got.Data != payload {
		t.Fatalf("update while backup paused: %v", err)
	}
	pause.resume()
	if err := <-backupDone; err != nil {
		t.Fatal(err)
	}
}

func TestInitialMmapSizeSharedFileOptions(t *testing.T) {
	ctx := context.Background()
	config := Config{Path: filepath.Join(t.TempDir(), "db"), InitialMmapSize: 16 << 20}
	s, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "job", "saved"); err != nil {
		t.Fatal(err)
	}
	peer, err := Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.Bind[string]("jobs"); err != nil {
		t.Fatal(err)
	}
	blobConfig := config
	blobConfig.Collection = "documents"
	blobs, err := OpenBlobs(ctx, blobConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	for _, size := range []int{0, 32 << 20} {
		other := config
		other.InitialMmapSize = size
		if raw, err := OpenRaw(ctx, other); !errors.Is(err, yuruidb.ErrInvalid) {
			if raw != nil {
				raw.Close()
			}
			t.Fatalf("mismatched record option %d: %v", size, err)
		}
		other.Collection = "documents"
		if store, err := OpenBlobs(ctx, other); !errors.Is(err, yuruidb.ErrInvalid) {
			if store != nil {
				store.Close()
			}
			t.Fatalf("mismatched Blob option %d: %v", size, err)
		}
	}
	s.Close()
	blobs.Close()
	if got, err := peer.Get[string](ctx, "job"); err != nil || got.Data != "saved" {
		t.Fatalf("shared owner affected: %+v %v", got, err)
	}
}

func TestInitialMmapSizeDefaultAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, err := OpenRaw(ctx, Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= 16<<20 {
		t.Fatalf("default unexpectedly preallocated a large file: %d", info.Size())
	}
	bad := Config{Path: filepath.Join(t.TempDir(), "invalid"), InitialMmapSize: -1}
	if _, err := OpenRaw(ctx, bad); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	bad.Collection = "documents"
	if _, err := OpenBlobs(ctx, bad); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := os.Stat(bad.Path); !os.IsNotExist(err) {
		t.Fatalf("invalid options created a file: %v", err)
	}
}
