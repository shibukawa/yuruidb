// Package d1 registers Cloudflare D1 via REST SQL. For application traffic the
// provider recommends a Worker gateway; account API rate limits apply here.
package d1

import (
	"context"
	"errors"
	"fmt"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
	"github.com/shibukawa/yuruidb/internal/httpx"
	"github.com/shibukawa/yuruidb/internal/sqlstore"
	"net/http"
	"net/url"
)

type Config struct {
	Endpoint, AccountID, DatabaseID, Collection, Table string
	Client                                             *http.Client
	Token                                              auth.TokenProvider
	Authorize                                          auth.Authorizer
	AutoMigrate                                        bool
}
type executor struct {
	client httpx.Client
	target string
}

func init() {
	yuruidb.Register("d1", func(ctx context.Context, v any) (yuruidb.Store, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return Open(ctx, c)
	})
}
func Open(ctx context.Context, c Config) (yuruidb.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.AccountID == "" || c.DatabaseID == "" {
		return nil, fmt.Errorf("%w: D1 account and database required", yuruidb.ErrInvalid)
	}
	if c.Endpoint == "" {
		c.Endpoint = "https://api.cloudflare.com/client/v4"
	}
	endpoint, err := httpx.Endpoint(c.Endpoint)
	if err != nil {
		return nil, err
	}
	authorize := c.Authorize
	if authorize == nil {
		authorize = auth.Bearer(c.Token)
	}
	e := &executor{client: httpx.Client{HTTP: c.Client, Auth: authorize}, target: endpoint + "/accounts/" + url.PathEscape(c.AccountID) + "/d1/database/" + url.PathEscape(c.DatabaseID) + "/query"}
	return sqlstore.Open(ctx, e, sqlstore.Config{Collection: c.Collection, Table: c.Table, Dialect: "sqlite", Scope: "d1:" + e.target, AutoMigrate: c.AutoMigrate, Distributed: true})
}
func (e *executor) run(ctx context.Context, q string, args []any, mutation bool) ([]map[string]any, int64, error) {
	var out struct {
		Success bool `json:"success"`
		Result  []struct {
			Success bool             `json:"success"`
			Results []map[string]any `json:"results"`
			Meta    struct {
				Changes *int64 `json:"changes"`
			} `json:"meta"`
		} `json:"result"`
	}
	if args == nil {
		args = []any{}
	}
	err := e.client.JSON(ctx, "POST", e.target, map[string]any{"sql": q, "params": args}, &out, nil, mutation)
	if err != nil {
		return nil, 0, err
	}
	if out.Success && len(out.Result) != 1 && mutation {
		return nil, 0, errors.Join(yuruidb.ErrOutcomeUnknown, fmt.Errorf("yuruidb: malformed D1 mutation result"))
	}
	if !out.Success || len(out.Result) != 1 || !out.Result[0].Success {
		return nil, 0, fmt.Errorf("yuruidb: D1 query failed")
	}
	r := out.Result[0]
	var changes int64
	if r.Meta.Changes != nil {
		changes = *r.Meta.Changes
	} else if mutation {
		return nil, 0, errors.Join(yuruidb.ErrOutcomeUnknown, fmt.Errorf("yuruidb: D1 mutation omitted row count"))
	}
	return r.Results, changes, nil
}
func (e *executor) Query(ctx context.Context, q string, args []any) ([]map[string]any, error) {
	r, _, err := e.run(ctx, q, args, false)
	return r, err
}
func (e *executor) Exec(ctx context.Context, q string, args []any) (int64, error) {
	_, n, err := e.run(ctx, q, args, true)
	return n, err
}
func (e *executor) Close() error { return nil }
