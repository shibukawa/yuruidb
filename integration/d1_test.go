package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/drivers/d1"
	"github.com/shibukawa/yuruidb/internal/conformance"
	_ "modernc.org/sqlite"
)

func TestRESTConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (yuruidb.RawStore, yuruidb.RawStore) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/accounts/account/d1/database/database/query" || r.Method != "POST" {
				t.Error("unexpected D1 endpoint")
			}
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("D1 bearer missing")
			}
			var in struct {
				SQL    string `json:"sql"`
				Params []any  `json:"params"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				http.Error(w, "json", 400)
				return
			}
			results := []map[string]any{}
			var changes int64
			if strings.HasPrefix(in.SQL, "SELECT") {
				rows, err := db.QueryContext(r.Context(), in.SQL, in.Params...)
				if err != nil {
					t.Error(err)
					http.Error(w, "SQL", 500)
					return
				}
				defer rows.Close()
				columns, _ := rows.Columns()
				for rows.Next() {
					v := make([]any, len(columns))
					p := make([]any, len(v))
					for i := range v {
						p[i] = &v[i]
					}
					if err = rows.Scan(p...); err != nil {
						t.Error(err)
					}
					row := map[string]any{}
					for i, name := range columns {
						row[name] = v[i]
					}
					results = append(results, row)
				}
			} else {
				result, err := db.ExecContext(r.Context(), in.SQL, in.Params...)
				if err != nil {
					t.Error(err)
					http.Error(w, "SQL", 500)
					return
				}
				changes, _ = result.RowsAffected()
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"success": true, "results": results, "meta": map[string]any{"changes": changes}}}})
		}))
		t.Cleanup(server.Close)
		open := func() yuruidb.RawStore {
			s, err := d1.OpenRaw(context.Background(), d1.Config{Endpoint: server.URL, AccountID: "account", DatabaseID: "database", Collection: "items", Token: auth.StaticToken("test-token"), AutoMigrate: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return s
		}
		return open(), open()
	})
}
