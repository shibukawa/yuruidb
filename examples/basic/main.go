package main

import (
	"context"
	"fmt"
	"github.com/shibukawa/yuruidb"
	_ "github.com/shibukawa/yuruidb/drivers/bbolt"
	"log"
	"os"
	"path/filepath"
)

type Note struct {
	Text string `json:"text"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "yuruidb-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{"Path": filepath.Join(dir, "records.db")})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Bind[Note]("notes"); err != nil {
		return err
	}
	id, err := yuruidb.NewID()
	if err != nil {
		return err
	}
	r, err := store.Insert(ctx, id, Note{Text: "hello"})
	if err != nil {
		return err
	}
	r, err = store.Update(ctx, r.ID(), Note{Text: "hello again"}, r.Revision())
	if err != nil {
		return err
	}
	page, err := store.List[Note](ctx, yuruidb.ListOptions{Limit: 10})
	if err != nil {
		return err
	}
	fmt.Printf("%d record: %s\n", len(page.Records), page.Records[0].Data.Text)
	return store.Delete[Note](ctx, r.ID(), r.Revision())
}
