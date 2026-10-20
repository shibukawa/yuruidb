package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync/atomic"

	"github.com/shibukawa/yuruidb"
)

type httpReader struct {
	store  *httpStore
	ctx    context.Context
	body   io.ReadCloser
	meta   Metadata
	closed atomic.Bool
}

func (r *httpReader) check() error {
	if r.closed.Load() {
		return yuruidb.ErrClosed
	}
	return r.store.check(r.ctx, r.meta.ID)
}
func (r *httpReader) Read(p []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.body.Read(p)
}
func (r *httpReader) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	return r.body.Close()
}
func (r *httpReader) ReadAt(p []byte, off int64) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	if off < 0 || int64(len(p)) > math.MaxInt64-off {
		return 0, yuruidb.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.meta.Size >= 0 && off >= r.meta.Size {
		return 0, io.EOF
	}
	req, err := http.NewRequestWithContext(r.ctx, "GET", r.store.config.ObjectURL(r.meta.ID), nil)
	if err != nil {
		return 0, yuruidb.ErrInvalid
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	if r.store.config.Generation {
		q := req.URL.Query()
		q.Set("ifGenerationMatch", r.meta.Revision)
		req.URL.RawQuery = q.Encode()
	} else {
		req.Header.Set("If-Match", r.meta.Revision)
	}
	response, err := r.store.request(req, false)
	if err != nil {
		var status *statusError
		if errors.As(err, &status) && status.status == http.StatusRequestedRangeNotSatisfiable {
			return 0, io.EOF
		}
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("%w: server ignored blob range", yuruidb.ErrUnsupported)
	}
	var start, end, size int64
	if _, err := fmt.Sscanf(response.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); err != nil || start != off || end < start || end != min(off+int64(len(p))-1, size-1) || size <= end || (r.meta.Size >= 0 && size != r.meta.Size) {
		return 0, fmt.Errorf("yuruidb: invalid blob Content-Range")
	}
	if token := r.store.token(response); token != "" && token != r.meta.Revision {
		return 0, yuruidb.ErrConflict
	}
	n, err := io.ReadFull(response.Body, p[:int(end-start+1)])
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}

func (s *httpStore) List(ctx context.Context, after string, n int) ([]Metadata, bool, error) {
	if s.config.List == nil {
		return nil, false, yuruidb.ErrUnsupported
	}
	return s.config.List(ctx, after, n)
}

func (s *httpStore) Remove(ctx context.Context, id string) error {
	if err := s.check(ctx, id); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "DELETE", s.config.ObjectURL(id), nil)
	if err != nil {
		return yuruidb.ErrInvalid
	}
	q := req.URL.Query()
	q.Del("alt")
	req.URL.RawQuery = q.Encode()
	response, err := s.request(req, true)
	if err == nil {
		response.Body.Close()
	}
	return err
}
