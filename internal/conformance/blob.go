package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/blob"
)

// RunBlobs exercises the shared document API. Each invocation needs independent
// storage; open must share that storage across its collection-specific handles.
func RunBlobs(t *testing.T, open func(string) (*blob.Store, error)) {
	t.Helper()
	ctx := context.Background()
	s, err := open("documents")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := bytes.Repeat([]byte("0123456789"), 15000)
	for _, id := range []string{"a", "b", "c"} {
		meta, err := s.Insert(ctx, id, bytes.NewReader(payload))
		if err != nil || meta.Size != int64(len(payload)) {
			t.Fatalf("insert: %+v %v", meta, err)
		}
	}
	if _, err := s.Insert(ctx, "a", strings.NewReader("replacement")); !errors.Is(err, yuruidb.ErrAlreadyExists) {
		t.Fatal(err)
	}
	other, err := open("other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if page, err := other.List(ctx, yuruidb.ListOptions{}); err != nil || len(page.Blobs) != 0 {
		t.Fatalf("collection isolation: %+v %v", page, err)
	}
	var cursor string
	for _, id := range []string{"a", "b", "c"} {
		page, err := s.List(ctx, yuruidb.ListOptions{Limit: 1, Cursor: cursor})
		if err != nil || len(page.Blobs) != 1 || page.Blobs[0].ID != id || page.Blobs[0].Size != int64(len(payload)) {
			t.Fatalf("list: %+v %v", page, err)
		}
		cursor = page.Cursor
		if id != "c" && cursor == "" {
			t.Fatal("missing next cursor")
		}
		if cursor != "" {
			if _, err := other.List(ctx, yuruidb.ListOptions{Cursor: cursor}); !errors.Is(err, yuruidb.ErrInvalid) {
				t.Fatalf("foreign cursor: %v", err)
			}
		}
	}
	if cursor != "" {
		t.Fatal("cursor after final page")
	}
	r, err := s.Open(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, offset := range []int64{0, 65530, 100000, int64(len(payload) - 8)} {
		wg.Add(1)
		go func(off int64) {
			defer wg.Done()
			buf := make([]byte, 20)
			n, err := r.ReadAt(buf, off)
			want := min(20, len(payload)-int(off))
			if n != want || !bytes.Equal(buf[:n], payload[off:off+int64(want)]) || (want < 20 && err != io.EOF) || (want == 20 && err != nil) {
				t.Errorf("range off=%d n=%d err=%v", off, n, err)
			}
		}(offset)
	}
	wg.Wait()
	if n, err := r.ReadAt(make([]byte, 1), int64(len(payload))); n != 0 || err != io.EOF {
		t.Fatalf("EOF: %d %v", n, err)
	}
	if _, err := r.ReadAt(make([]byte, 1), -1); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(r); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("stream: %v", err)
	}
	if err := s.Delete(ctx, "b", "wrong"); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "b", r.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, "b", strings.NewReader("new bytes")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, yuruidb.ErrConflict) {
		t.Fatalf("range read replacement: %v", err)
	}
	r.Close()
	if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, yuruidb.ErrClosed) {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "b"); !errors.Is(err, yuruidb.ErrNotFound) {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	r, err = s.Open(cancelCtx, "a")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := r.ReadAt(make([]byte, 1), 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, yuruidb.ListOptions{}); !errors.Is(err, yuruidb.ErrClosed) {
		t.Fatal(err)
	}
}
