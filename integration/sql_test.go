package integration_test

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/shibukawa/yuruidb"
	driver "github.com/shibukawa/yuruidb/drivers/sql"
	"github.com/shibukawa/yuruidb/internal/conformance"
	_ "modernc.org/sqlite"
)

func TestSQLiteConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		dsn := (&url.URL{
			Scheme:   "file",
			Path:     filepath.Join(t.TempDir(), "db"),
			RawQuery: url.Values{"_pragma": {"busy_timeout(10000)", "journal_mode(WAL)"}}.Encode(),
		}).String()
		open := func() yuruidb.RawStore {
			db, err := sql.Open("sqlite", dsn)
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
