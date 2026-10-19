// Package r2 registers Cloudflare R2 via its S3-compatible HTTP API.
package r2

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/s3api"
)

type Config = s3api.Config

func Open(ctx context.Context, c Config) (yuruidb.Store, error) { return s3api.Open(ctx, c, "r2") }
func init() {
	yuruidb.Register("r2", func(ctx context.Context, v any) (yuruidb.Store, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return Open(ctx, c)
	})
}
