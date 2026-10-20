package sqlstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/recordstore"
	"regexp"
	"strings"
)

type Executor interface {
	Query(context.Context, string, []any) ([]map[string]any, error)
	Exec(context.Context, string, []any) (int64, error)
	Close() error
}
type Config struct {
	Collection, Table, Dialect, Scope string
	AutoMigrate                       bool
	Distributed                       bool
}
type transport struct {
	exec                               Executor
	table, collection, prefix, dialect string
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

func Open(ctx context.Context, e Executor, c Config) (yuruidb.RawStore, error) {
	if c.Collection != "" {
		if err := yuruidb.ValidateCollection(c.Collection); err != nil {
			return nil, err
		}
	}
	if c.Table == "" {
		c.Table = "ururidb_records"
	}
	if !identifier.MatchString(c.Table) {
		return nil, fmt.Errorf("%w: invalid SQL table identifier", yuruidb.ErrInvalid)
	}
	if c.Dialect == "" {
		c.Dialect = "sqlite"
	}
	if c.Dialect != "sqlite" && c.Dialect != "postgres" {
		return nil, fmt.Errorf("%w: SQL dialect %q", yuruidb.ErrUnsupported, c.Dialect)
	}
	t := &transport{exec: e, table: `"` + c.Table + `"`, collection: c.Collection, prefix: c.Collection + "/", dialect: c.Dialect}
	if c.AutoMigrate {
		collation := "BINARY"
		if c.Dialect == "postgres" {
			collation = `"C"`
		}
		schema := "CREATE TABLE IF NOT EXISTS " + t.table + " (collection TEXT NOT NULL, id TEXT COLLATE " + collation + " NOT NULL, payload TEXT NOT NULL, token TEXT NOT NULL, PRIMARY KEY (collection, id))"
		if _, err := e.Exec(ctx, schema, nil); err != nil {
			return nil, err
		}
	}
	return recordstore.New(t, c.Collection, c.Scope+":"+c.Table+":"+c.Dialect, yuruidb.Capabilities{ConditionalMutations: true, Distributed: c.Distributed, StrongReads: true})
}
func (t *transport) bind(query string) string {
	if t.dialect != "postgres" {
		return query
	}
	i := 0
	return regexp.MustCompile(`\?`).ReplaceAllStringFunc(query, func(string) string { i++; return fmt.Sprintf("$%d", i) })
}
func text(v any) (string, error) {
	switch value := v.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", fmt.Errorf("yuruidb: SQL response is not text")
	}
}
func (t *transport) Read(ctx context.Context, key string) (recordstore.Item, error) {
	rows, err := t.exec.Query(ctx, t.bind("SELECT payload, token FROM "+t.table+" WHERE collection = ? AND id = ?"), []any{t.collection, key})
	if err != nil {
		return recordstore.Item{}, err
	}
	if len(rows) == 0 {
		return recordstore.Item{}, yuruidb.ErrNotFound
	}
	data, err := text(rows[0]["payload"])
	if err != nil {
		return recordstore.Item{}, err
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return recordstore.Item{}, fmt.Errorf("yuruidb: corrupt SQL payload")
	}
	token, err := text(rows[0]["token"])
	return recordstore.Item{Data: b, Token: token}, err
}
func (t *transport) Write(ctx context.Context, key string, data []byte, expected string) error {
	_, err := t.WriteItem(ctx, key, data, expected)
	return err
}
func (t *transport) WriteItem(ctx context.Context, key string, data []byte, expected string) (string, error) {
	token, err := yuruidb.NewRevision()
	if err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString(data)
	var query string
	var args []any
	if expected == "" {
		query = "INSERT INTO " + t.table + " (collection, id, payload, token) VALUES (?, ?, ?, ?) ON CONFLICT (collection, id) DO NOTHING"
		args = []any{t.collection, key, payload, token}
	} else {
		query = "UPDATE " + t.table + " SET payload = ?, token = ? WHERE collection = ? AND id = ? AND token = ?"
		args = []any{payload, token, t.collection, key, expected}
	}
	changed, err := t.exec.Exec(ctx, t.bind(query), args)
	if err != nil {
		return "", err
	}
	if changed == 0 {
		return "", yuruidb.ErrConflict
	}
	if changed != 1 {
		return "", fmt.Errorf("yuruidb: SQL mutation affected unexpected row count")
	}
	return token, nil
}
func (t *transport) Keys(ctx context.Context, after string, n int) ([]string, bool, error) {
	rows, err := t.exec.Query(ctx, t.bind("SELECT id FROM "+t.table+" WHERE collection = ? AND id > ? ORDER BY id ASC LIMIT ?"), []any{t.collection, after, n + 1})
	if err != nil {
		return nil, false, err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		k, err := text(row["id"])
		if err != nil {
			return nil, false, err
		}
		if !strings.HasPrefix(k, t.prefix) {
			return nil, false, fmt.Errorf("yuruidb: SQL key outside collection")
		}
		keys = append(keys, k)
	}
	more := len(keys) > n
	if more {
		keys = keys[:n]
	}
	return keys, more, nil
}
func (t *transport) Close() error { return t.exec.Close() }

func (t *transport) ForCollection(ctx context.Context, name string) (recordstore.Transport, error) {
	copy := *t
	copy.collection = name
	copy.prefix = name + "/"
	return &copy, nil
}

func (t *transport) Delete(ctx context.Context, key, expected string) error {
	changed, err := t.exec.Exec(ctx, t.bind("DELETE FROM "+t.table+" WHERE collection = ? AND id = ? AND token = ?"), []any{t.collection, key, expected})
	if err != nil {
		return err
	}
	if changed == 0 {
		return yuruidb.ErrConflict
	}
	if changed != 1 {
		return fmt.Errorf("yuruidb: unexpected deleted row count")
	}
	return nil
}
func (t *transport) Scan(ctx context.Context, after string, n int) ([]recordstore.Entry, bool, error) {
	rows, err := t.exec.Query(ctx, t.bind("SELECT id, payload, token FROM "+t.table+" WHERE collection = ? AND id > ? ORDER BY id ASC LIMIT ?"), []any{t.collection, after, n + 1})
	if err != nil {
		return nil, false, err
	}
	out := make([]recordstore.Entry, 0, len(rows))
	for _, row := range rows {
		key, err := text(row["id"])
		if err != nil {
			return nil, false, err
		}
		payload, err := text(row["payload"])
		if err != nil {
			return nil, false, err
		}
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false, err
		}
		token, err := text(row["token"])
		if err != nil {
			return nil, false, err
		}
		out = append(out, recordstore.Entry{Key: key, Item: recordstore.Item{Data: b, Token: token}})
	}
	more := len(out) > n
	if more {
		out = out[:n]
	}
	return out, more, nil
}
