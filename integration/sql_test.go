package integration_test

import (
	"context"
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shibukawa/yuruidb"
	driver "github.com/shibukawa/yuruidb/drivers/sql"
	"github.com/shibukawa/yuruidb/internal/conformance"
	"path/filepath"
	"testing"
)

func TestSQLiteConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		dsn := filepath.Join(t.TempDir(), "db") + "?_busy_timeout=10000&_journal_mode=WAL"
		open := func() yuruidb.RawStore {
			db, err := sql.Open("sqlite3", dsn)
			if err != nil {
				t.Fatal(err)
			}
			s, err := driver.OpenRaw(context.Background(), driver.Config{DB: db, Collection: "items", AutoMigrate: true, CloseDB: true})
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
