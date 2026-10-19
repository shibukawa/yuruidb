// Package s3 registers S3 general-purpose buckets via a signed HTTP API.
package s3

import (
	"context"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/internal/s3api"
)

type Config = s3api.Config

func OpenRaw(ctx context.Context, c Config) (yuruidb.RawStore, error) {
	return s3api.Open(ctx, c, "s3")
}
func init() {
	yuruidb.Register("s3", func(ctx context.Context, v any) (yuruidb.RawStore, error) {
		c, err := yuruidb.DecodeConfig[Config](v)
		if err != nil {
			return nil, err
		}
		return OpenRaw(ctx, c)
	})
}

// Open opens a JSON-typed collection using this driver.
func Open[T any](ctx context.Context, c Config) (*yuruidb.Store[T], error) {
	raw, err := OpenRaw(ctx, c)
	if err != nil {
		return nil, err
	}
	return yuruidb.Wrap[T](raw), nil
}
