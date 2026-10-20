package blob_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/blob"
)

type countedBody struct {
	io.Reader
	closes *atomic.Int32
}

func (b countedBody) Close() error { b.closes.Add(1); return nil }

func TestHTTPOpenDefersSequentialBody(t *testing.T) {
	for _, generation := range []bool{false, true} {
		for _, mode := range []string{"range-only", "sequential", "close-only", "replaced"} {
			t.Run(fmt.Sprintf("generation=%v/%s", generation, mode), func(t *testing.T) {
				var metadata, fullGets, ranges, metaClosed, bodyClosed atomic.Int32
				version := "v1"
				s, err := blob.NewHTTP(blob.HTTPConfig{Generation: generation, ObjectURL: func(string) string { return "https://example.invalid/document?alt=media" }, UploadURL: func(string) string { return "https://example.invalid/document" }, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					h := http.Header{"Etag": {version}, "X-Goog-Generation": {version}}
					if r.Method == "HEAD" || (generation && r.URL.Query().Get("alt") == "") {
						metadata.Add(1)
						body := ""
						if generation {
							body = `{"generation":"v1","size":"8"}`
						}
						return &http.Response{StatusCode: 200, Header: h, ContentLength: 8, Body: countedBody{strings.NewReader(body), &metaClosed}}, nil
					}
					if (generation && r.URL.Query().Get("ifGenerationMatch") != "v1") || (!generation && r.Header.Get("If-Match") != "v1") {
						t.Error("body request missing original version condition")
					}
					if version != "v1" {
						return &http.Response{StatusCode: 412, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					if value := r.Header.Get("Range"); value != "" {
						ranges.Add(1)
						if value != "bytes=2-5" {
							t.Errorf("range=%s", value)
						}
						h.Set("Content-Range", "bytes 2-5/8")
						return &http.Response{StatusCode: 206, Header: h, ContentLength: 4, Body: io.NopCloser(strings.NewReader("cdef"))}, nil
					}
					fullGets.Add(1)
					return &http.Response{StatusCode: 200, Header: h, ContentLength: 8, Body: countedBody{strings.NewReader("abcdefgh"), &bodyClosed}}, nil
				})}})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				r, err := s.Open(context.Background(), "document")
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				if metadata.Load() != 1 || metaClosed.Load() != 1 || fullGets.Load() != 0 {
					t.Fatalf("Open downloaded or retained a body: metadata=%d closed=%d full=%d", metadata.Load(), metaClosed.Load(), fullGets.Load())
				}
				if _, err := r.Read(nil); err != nil {
					t.Fatal(err)
				}
				if fullGets.Load() != 0 {
					t.Fatal("empty Read started a body request")
				}
				switch mode {
				case "range-only", "sequential":
					buf := make([]byte, 4)
					if n, err := r.ReadAt(buf, 2); err != nil || n != 4 || string(buf) != "cdef" {
						t.Fatalf("range: %s %d %v", buf, n, err)
					}
					if fullGets.Load() != 0 || ranges.Load() != 1 {
						t.Fatal("ReadAt opened a sequential response")
					}
					if mode == "sequential" {
						if data, err := io.ReadAll(r); err != nil || string(data) != "abcdefgh" {
							t.Fatalf("stream: %s %v", data, err)
						}
					}
				case "replaced":
					version = "v2"
					if _, err := r.Read(make([]byte, 1)); !errors.Is(err, yuruidb.ErrConflict) {
						t.Fatalf("lazy GET ignored replacement: %v", err)
					}
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				if mode == "sequential" {
					if fullGets.Load() != 1 || bodyClosed.Load() != 1 {
						t.Fatal("sequential response was not reused and closed")
					}
				} else if fullGets.Load() != 0 {
					t.Fatal("unused sequential response opened")
				}
			})
		}
	}
}

type blockedHTTPBody struct {
	ctx     context.Context
	entered chan struct{}
}

func (b blockedHTTPBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b blockedHTTPBody) Close() error { return nil }

func TestLazyHTTPReaderCloseInterruptsRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	s, err := blob.NewHTTP(blob.HTTPConfig{ObjectURL: func(string) string { return "https://example.invalid/document" }, UploadURL: func(string) string { return "https://example.invalid/document" }, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var body io.ReadCloser = io.NopCloser(strings.NewReader(""))
		if r.Method == "GET" {
			body = blockedHTTPBody{r.Context(), entered}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Etag": {"v1"}}, ContentLength: 8, Body: body}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Open(ctx, "document")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	readDone := make(chan error, 1)
	go func() { _, err := r.Read(make([]byte, 1)); readDone <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- r.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-closeDone
		t.Fatal("Close failed to interrupt Read")
	}
	if err := <-readDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Read returned %v", err)
	}
}
