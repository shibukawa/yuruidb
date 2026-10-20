// Package blob provides immutable streaming documents, kept in a separate
// collection from JSON records. Use fresh IDs (for example NewID) for documents;
// put their references in small typed records. It provides no multi-key commit.
package blob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/auth"
)

type Metadata struct {
	ID, Revision string
	Size         int64
}
type Reader struct {
	io.ReadCloser
	io.ReaderAt
	Metadata
}

// HTTPConfig is the streaming integration boundary used by object drivers.
type HTTPConfig struct {
	Client            *http.Client
	Authorize         auth.Authorizer
	ObjectURL         func(string) string
	UploadURL         func(string) string
	Generation        bool
	ConditionalDelete bool
	List              func(context.Context, string, int) ([]Metadata, bool, error)
}
type httpStore struct {
	config HTTPConfig
	closed atomic.Bool
}

type statusError struct{ status int }

func (e *statusError) Error() string { return fmt.Sprintf("yuruidb: blob HTTP %d", e.status) }

func NewHTTP(config HTTPConfig) (*Store, error) {
	if config.ObjectURL == nil || config.UploadURL == nil {
		return nil, yuruidb.ErrInvalid
	}
	if config.Client == nil {
		config.Client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return NewBackend(&httpStore{config: config}, config.ObjectURL(""))
}
func (s *httpStore) check(ctx context.Context, id string) error {
	if s.closed.Load() {
		return yuruidb.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return yuruidb.ValidateID(id)
}
func (s *httpStore) Close() error { s.closed.Store(true); return nil }
func (s *httpStore) request(req *http.Request, mutation bool) (*http.Response, error) {
	if s.config.Authorize != nil {
		if err := s.config.Authorize(req); err != nil {
			return nil, err
		}
	}
	response, err := s.config.Client.Do(req)
	if err != nil {
		cause := error(fmt.Errorf("yuruidb: blob transport failed"))
		if req.Context().Err() != nil {
			cause = req.Context().Err()
		}
		if mutation {
			cause = errors.Join(yuruidb.ErrOutcomeUnknown, cause)
		}
		return nil, cause
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	response.Body.Close()
	cause := &statusError{response.StatusCode}
	switch response.StatusCode {
	case 404:
		return nil, errors.Join(yuruidb.ErrNotFound, cause)
	case 409, 412:
		return nil, errors.Join(yuruidb.ErrConflict, cause)
	}
	if mutation && response.StatusCode >= 500 {
		return nil, errors.Join(yuruidb.ErrOutcomeUnknown, cause)
	}
	return nil, cause
}
func (s *httpStore) token(response *http.Response) string {
	if s.config.Generation {
		return response.Header.Get("X-Goog-Generation")
	}
	return response.Header.Get("ETag")
}

// Open reads metadata only. A sequential GET is deferred until the first Read;
// callers using only ReadAt never open a full-document response body.
func (s *httpStore) Open(ctx context.Context, id string) (*Reader, error) {
	if err := s.check(ctx, id); err != nil {
		return nil, err
	}
	method, target := "HEAD", s.config.ObjectURL(id)
	if s.config.Generation {
		// The GCS JSON object resource contains generation and size. Its media
		// URL is only used once a body or range is actually requested.
		method = "GET"
		req, err := http.NewRequestWithContext(ctx, method, target, nil)
		if err != nil {
			return nil, yuruidb.ErrInvalid
		}
		q := req.URL.Query()
		q.Del("alt")
		req.URL.RawQuery = q.Encode()
		target = req.URL.String()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, yuruidb.ErrInvalid
	}
	req.Header.Set("Accept-Encoding", "identity")
	response, err := s.request(req, false)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	token := s.token(response)
	size := response.ContentLength
	if s.config.Generation {
		var out struct{ Generation, Size string }
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil {
			return nil, fmt.Errorf("yuruidb: invalid Blob metadata")
		}
		token = out.Generation
		size, err = strconv.ParseInt(out.Size, 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("yuruidb: invalid Blob size")
		}
	}
	if token == "" {
		return nil, fmt.Errorf("yuruidb: blob response lacks version")
	}
	meta := Metadata{ID: id, Revision: token, Size: size}
	readCtx, cancel := context.WithCancel(ctx)
	r := &httpReader{store: s, ctx: readCtx, cancel: cancel, meta: meta}
	return &Reader{ReadCloser: r, ReaderAt: r, Metadata: meta}, nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

// Insert creates an absent document. Upload input is spooled to a temporary file
// so SigV4 can hash and replay it without loading the document into memory. The
// temporary file is removed on every return path. Existing IDs are never replaced.
func (s *httpStore) Insert(ctx context.Context, id string, source io.Reader) (Metadata, error) {
	if err := s.check(ctx, id); err != nil {
		return Metadata{}, err
	}
	if source == nil {
		return Metadata{}, yuruidb.ErrInvalid
	}
	file, err := os.CreateTemp("", "yuruidb-blob-*")
	if err != nil {
		return Metadata{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	size, err := io.Copy(file, contextReader{ctx: ctx, source: source})
	if err != nil {
		return Metadata{}, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, err
	}
	method := "PUT"
	if s.config.Generation {
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, s.config.UploadURL(id), file)
	if err != nil {
		return Metadata{}, yuruidb.ErrInvalid
	}
	req.ContentLength = size
	req.GetBody = func() (io.ReadCloser, error) { return os.Open(file.Name()) }
	req.Header.Set("Content-Type", "application/octet-stream")
	if !s.config.Generation {
		req.Header.Set("If-None-Match", "*")
	}
	response, err := s.request(req, true)
	if errors.Is(err, yuruidb.ErrConflict) {
		return Metadata{}, errors.Join(yuruidb.ErrAlreadyExists, err)
	}
	if err != nil {
		return Metadata{}, err
	}
	defer response.Body.Close()
	token := s.token(response)
	if s.config.Generation {
		var out struct {
			Generation string `json:"generation"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out) != nil {
			return Metadata{}, yuruidb.ErrOutcomeUnknown
		}
		token = out.Generation
	}
	if token == "" {
		return Metadata{}, fmt.Errorf("%w: blob mutation lacks version", yuruidb.ErrOutcomeUnknown)
	}
	return Metadata{ID: id, Revision: token, Size: size}, nil
}

// Delete requires the native version returned by Open or Insert. Object IDs
// should not be reused; unlike record revisions, ETags may repeat for equal bytes.
func (s *httpStore) Delete(ctx context.Context, id, revision string) error {
	if err := s.check(ctx, id); err != nil {
		return err
	}
	if revision == "" {
		return yuruidb.ErrInvalid
	}
	if !s.config.ConditionalDelete {
		return yuruidb.ErrUnsupported
	}
	target := s.config.ObjectURL(id)
	req, err := http.NewRequestWithContext(ctx, "DELETE", target, nil)
	if err != nil {
		return yuruidb.ErrInvalid
	}
	if s.config.Generation {
		q := req.URL.Query()
		q.Del("alt")
		q.Set("ifGenerationMatch", revision)
		req.URL.RawQuery = q.Encode()
	} else {
		req.Header.Set("If-Match", revision)
	}
	response, err := s.request(req, true)
	if err == nil {
		response.Body.Close()
	}
	return err
}
