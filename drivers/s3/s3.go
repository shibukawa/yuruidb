// Package s3 registers S3 general-purpose buckets via a signed HTTP API.
package s3

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/s3api"
)

type Config = s3api.Config

func Open(ctx context.Context, c Config) (yuruidb.Store, error) { return s3api.Open(ctx, c, "s3") }
func init() {
	yuruidb.Register("s3", func(ctx context.Context, v any) (yuruidb.Store, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return Open(ctx, c)
	})
}
