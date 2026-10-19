// Package sql registers the "sql" driver over an injected database/sql DB.
// SQLite and PostgreSQL dialects are supported. Import the database vendor's
// driver in the application. This package does not bundle a database driver.
package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/sqlstore"
)

type Config struct {
	DB                         *sql.DB
	Collection, Table, Dialect string
	AutoMigrate, CloseDB       bool
}
type executor struct {
	db    *sql.DB
	close bool
}

func init() {
	yuruidb.Register("sql", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return OpenRaw(ctx, c)
	})
}
func OpenRaw(ctx context.Context, c Config) (yuruidb.RawStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.DB == nil {
		return nil, fmt.Errorf("%w: SQL DB required", yuruidb.ErrInvalid)
	}
	return sqlstore.Open(ctx, &executor{c.DB, c.CloseDB}, sqlstore.Config{Collection: c.Collection, Table: c.Table, Dialect: c.Dialect, Scope: "sql", AutoMigrate: c.AutoMigrate, Distributed: c.Dialect == "postgres"})
}
func (e *executor) Query(ctx context.Context, q string, args []any) ([]map[string]any, error) {
	rows, err := e.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, column := range columns {
			if b, ok := values[i].([]byte); ok {
				row[column] = string(b)
			} else {
				row[column] = values[i]
			}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
func (e *executor) Exec(ctx context.Context, q string, args []any) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	result, err := e.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, errors.Join(yuruidb.ErrOutcomeUnknown, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Join(yuruidb.ErrOutcomeUnknown, err)
	}
	return count, nil
}
func (e *executor) Close() error {
	if e.close {
		return e.db.Close()
	}
	return nil
}

// Open opens a JSON-typed collection using this driver.
func Open[T any](ctx context.Context, c Config) (*yuruidb.Store[T], error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap[T](raw), nil
}
