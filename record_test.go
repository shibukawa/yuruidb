package yuruidb_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shibukawa/yuruidb"
	"github.com/shibukawa/yuruidb/drivers/memory"
)

func TestRecordMetadata(t *testing.T) {
	ctx := context.Background()
	s, err := memory.Open[note](ctx, memory.Config{Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := nextID(t)
	original, err := s.Insert(ctx, id, note{Text: "original"})
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := original.Revision()
	edited := original
	edited.Data.Text = "updated"
	updated, err := s.Update(ctx, edited.ID(), edited.Data, edited.Revision())
	if err != nil {
		t.Fatal(err)
	}
	if original.ID() != id || original.Revision() != oldRevision || original.Data.Text != "original" {
		t.Fatalf("original record changed: %+v", original)
	}
	if updated.ID() != id || updated.Revision() == oldRevision || updated.Data.Text != "updated" {
		t.Fatalf("updated record: %+v", updated)
	}
	// The application can edit Data, while metadata has no exported fields.
	recordType := reflect.TypeFor[yuruidb.Record[note]]()
	for i := 0; i < recordType.NumField(); i++ {
		field := recordType.Field(i)
		if field.IsExported() && field.Name != "Data" {
			t.Fatalf("writable metadata field: %s", field.Name)
		}
	}
}

func TestRecordJSON(t *testing.T) {
	ctx := context.Background()
	s, err := memory.Open[note](ctx, memory.Config{Collection: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	original, err := s.Insert(ctx, nextID(t), note{Text: "hello", Labels: map[string]string{"lang": "ja"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
		Data     note   `json:"data"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.ID != original.ID() || wire.Revision != original.Revision() || !reflect.DeepEqual(wire.Data, original.Data) {
		t.Fatalf("JSON omitted metadata or data: %s", encoded)
	}
	var restored yuruidb.Record[note]
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("round trip: %+v vs %+v", restored, original)
	}
	// A partially decoded map must not leak changes into the existing record.
	if err := json.Unmarshal([]byte(`{"id":"changed","data":{"labels":{"lang":"en"},"text":42}}`), &restored); err == nil {
		t.Fatal("invalid data type was accepted")
	}
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("failed decoding changed existing record: %+v", restored)
	}
}
