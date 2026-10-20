package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/lib/pq"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/blob"
	driver "github.com/shibukawa/yuruidb/drivers/sql"
	"github.com/shibukawa/yuruidb/internal/conformance"
)

func sqlBlobConfig(t *testing.T, dialect string) driver.Config {
	t.Helper()
	dsn, vendor := os.Getenv("YURUIDB_POSTGRES_DSN"), "postgres"
	if dialect == "sqlite" {
		vendor = "sqlite"
		dsn = (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "db"), RawQuery: url.Values{"_pragma": {"busy_timeout(10000)", "journal_mode(WAL)"}}.Encode()}).String()
	} else if dsn == "" {
		t.Skip("set YURUIDB_POSTGRES_DSN to run live PostgreSQL Blob tests")
	}
	db, err := sql.Open(vendor, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	config := driver.Config{DB: db, Dialect: dialect, AutoMigrate: true, Collection: "documents", Identity: t.Name()}
	if dialect == "postgres" {
		token, err := yuruidb.NewRevision()
		if err != nil {
			t.Fatal(err)
		}
		config.Table = "yuruidb_blob_test_" + token[:16]
		t.Cleanup(func() {
			if _, err := db.ExecContext(context.Background(), `DROP TABLE IF EXISTS "`+config.Table+`"`); err != nil {
				t.Error(err)
			}
		})
	}
	return config
}

func TestSQLBlobConformance(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			config := sqlBlobConfig(t, dialect)
			conformance.RunBlobs(t, func(collection string) (*blob.Store, error) {
				c := config
				c.Collection = collection
				return driver.OpenBlobs(context.Background(), c)
			})
			if err := config.DB.PingContext(context.Background()); err != nil {
				t.Fatalf("borrowed DB was closed: %v", err)
			}
		})
	}
}

func TestSQLBlobBinaryStorageAndAtomicCreate(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			config := sqlBlobConfig(t, dialect)
			s, err := driver.OpenBlobs(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			payload := make([]byte, 256*600)
			for i := range payload {
				payload[i] = byte(i)
			}
			meta, err := s.Insert(ctx, "binary", bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.Open(ctx, meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("binary streaming: %v", err)
			}
			buf := make([]byte, 24)
			if n, err := r.ReadAt(buf, 250); err != nil || n != 24 || !bytes.Equal(buf, payload[250:274]) {
				t.Fatalf("binary byte offsets: n=%d err=%v", n, err)
			}
			if n, err := r.ReadAt(buf, math.MaxInt64); n != 0 || err != io.EOF {
				t.Fatalf("large offset: n=%d err=%v", n, err)
			}
			r.Close()
			if _, err := s.Insert(ctx, "empty", strings.NewReader("")); err != nil {
				t.Fatal(err)
			}
			empty, err := s.Open(ctx, "empty")
			if err != nil {
				t.Fatal(err)
			}
			if data, err := io.ReadAll(empty); err != nil || len(data) != 0 || empty.Size != 0 {
				t.Fatalf("empty blob: %v", err)
			}
			empty.Close()
			table := config.Table
			if table == "" {
				table = "ururidb_blobs"
			}
			typeExpr, wantType := "typeof(payload)", "blob"
			if dialect == "postgres" {
				typeExpr, wantType = "pg_typeof(payload)::text", "bytea"
			}
			var storedType string
			if err := config.DB.QueryRowContext(ctx, `SELECT `+typeExpr+` FROM "`+table+`" WHERE id = 'binary'`).Scan(&storedType); err != nil || storedType != wantType {
				t.Fatalf("native binary type=%q err=%v", storedType, err)
			}
			if _, err := s.Insert(ctx, "failed", io.MultiReader(bytes.NewReader(payload), brokenBlobInput{})); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("source failure: %v", err)
			}
			if _, err := s.Open(ctx, "failed"); !errors.Is(err, yuruidb.ErrNotFound) {
				t.Fatalf("partial insert visible: %v", err)
			}
			var wins atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.Insert(ctx, "race", bytes.NewReader(payload))
					if err == nil {
						wins.Add(1)
					} else if !errors.Is(err, yuruidb.ErrAlreadyExists) {
						t.Errorf("concurrent insert: %v", err)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("concurrent insert winners=%d", wins.Load())
			}
			// Reopen without schema creation, and ensure stale versions cannot delete
			// an ID recreated after removal, even when its contents are identical.
			config.AutoMigrate = false
			peer, err := driver.OpenBlobs(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			if err := peer.Remove(ctx, meta.ID); err != nil {
				t.Fatal(err)
			}
			next, err := peer.Insert(ctx, meta.ID, bytes.NewReader(payload))
			if err != nil || next.Revision == meta.Revision {
				t.Fatalf("recreate: %+v %v", next, err)
			}
			if err := s.Delete(ctx, meta.ID, meta.Revision); !errors.Is(err, yuruidb.ErrConflict) {
				t.Fatalf("ABA delete: %v", err)
			}
		})
	}
}

type brokenBlobInput struct{}

func (brokenBlobInput) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSQLBlobRecordIsolationAndOwnership(t *testing.T) {
	ctx := context.Background()
	config := sqlBlobConfig(t, "sqlite")
	records, err := driver.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer records.Close()
	if err := records.Bind[string]("documents"); err != nil {
		t.Fatal(err)
	}
	if _, err := records.Insert(ctx, "same-id", "record"); err != nil {
		t.Fatal(err)
	}
	blobs, err := driver.OpenBlobs(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Insert(ctx, "same-id", strings.NewReader("blob")); err != nil {
		t.Fatal(err)
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := records.Get[string](ctx, "same-id"); err != nil || got.Data != "record" {
		t.Fatalf("record/blob isolation: %+v %v", got, err)
	}
	ownedConfig := sqlBlobConfig(t, "sqlite")
	ownedConfig.CloseDB = true
	owned, err := driver.OpenBlobs(ctx, ownedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ownedConfig.DB.PingContext(ctx); err == nil {
		t.Fatal("owned DB was not closed")
	}
}
