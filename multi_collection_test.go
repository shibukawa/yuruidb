package yuruidb_test

import (
	"context"
	"errors"
	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/bbolt"
	"github.com/shibukawa/yuruidb/drivers/memory"
	"path/filepath"
	"sync"
	"testing"
)

type spoolJob struct {
	Text string `json:"text"`
}
type Waiting spoolJob
type Done spoolJob
type spoolMeta spoolJob
type spoolPrinter spoolJob
type spoolTrust spoolJob

func TestMultiCollectionBbolt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "spool.db")
	open := func() *yuruidb.Store {
		s, err := bbolt.Open(ctx, bbolt.Config{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	s := open()
	binds := []error{s.Bind[Waiting]("jobs"), s.Bind[Done]("done"), s.Bind[spoolMeta]("meta"), s.Bind[spoolPrinter]("printers"), s.Bind[spoolTrust]("trust")}
	for _, err := range binds {
		if err != nil {
			t.Fatal(err)
		}
	}
	waiting, err := s.Insert(ctx, "host.local", Waiting{Text: "waiting"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Insert(ctx, "host.local", Done{Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, "host.local", spoolMeta{Text: "meta"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, "host.local", spoolPrinter{Text: "printer"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Insert(ctx, "host.local", spoolTrust{Text: "trust"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get[*Waiting](ctx, "host.local")
	if err != nil || got.Data == nil || got.Data.Text != "waiting" {
		t.Fatalf("pointer lookup: %+v %v", got, err)
	}
	if _, err = s.Update(ctx, "host.local", Done{}, waiting.Revision()); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatalf("foreign revision accepted: %v", err)
	}
	p, err := s.List[Done](ctx, yuruidb.ListOptions{})
	if err != nil || len(p.Records) != 1 || p.Records[0] != done {
		t.Fatalf("list: %+v %v", p, err)
	}
	other := open()
	if err := other.Bind[Waiting]("jobs"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	got2, err := other.Get[Waiting](ctx, "host.local")
	if err != nil || got2.Data != waiting.Data {
		t.Fatalf("shared connection closed: %+v %v", got2, err)
	}
	other.Close()
	reopened := open()
	if err := reopened.Bind[Done]("done"); err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Get[Done](ctx, "host.local")
	if err != nil || saved != done {
		t.Fatalf("reopen: %+v %v", saved, err)
	}
	if _, err = s.Get[Waiting](ctx, "host.local"); !errors.Is(err, yuruidb.ErrClosed) {
		t.Fatal(err)
	}
}
func TestBindingsAndParallelCollections(t *testing.T) {
	ctx := context.Background()
	s, err := memory.Open(ctx, memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Get[Waiting](ctx, "id"); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Bind[Waiting]("jobs"); err != nil {
		t.Fatal(err)
	}
	if err = s.Bind[*Waiting]("jobs"); err != nil {
		t.Fatal(err)
	}
	type WaitingAlias = Waiting
	if err = s.Bind[WaitingAlias]("jobs"); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{s.Bind[Waiting]("other"), s.Bind[Done]("jobs"), s.Bind[Done]("bad/name")} {
		if !errors.Is(err, yuruidb.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if err = s.Bind[Done]("done"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := yuruidb.NewID()
			if err != nil {
				t.Error(err)
				return
			}
			if i%2 == 0 {
				_, err = s.Insert(ctx, id, Waiting{Text: id})
			} else {
				_, err = s.Insert(ctx, id, Done{Text: id})
			}
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	p, err := s.List[Waiting](ctx, yuruidb.ListOptions{Limit: 1})
	if err != nil || p.Cursor == "" {
		t.Fatalf("cursor: %+v %v", p, err)
	}
	if _, err = s.List[Done](ctx, yuruidb.ListOptions{Cursor: p.Cursor}); !errors.Is(err, yuruidb.ErrInvalid) {
		t.Fatalf("foreign cursor: %v", err)
	}
}
