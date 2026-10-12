package main

import (
	"github.com/Azure-DocumentDB/vector-search-go/internal/documentdb"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPrecomputedFixture(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "data.json")
	output := filepath.Join(dir, "query.json")
	if err := os.WriteFile(input, []byte(`[{"HotelId":"1","DescriptionVector":[1,0]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(input, output, "DescriptionVector", "HotelId", 2); err != nil {
		t.Fatal(err)
	}
	if vector, err := documentdb.ReadQueryVector(output, 2); err != nil || vector[0] != 1 {
		t.Fatal("wrong extracted query")
	}
	if err := run(input, output, "DescriptionVector", "HotelId", 2); err == nil {
		t.Fatal("existing query file overwritten")
	}
	if err := run(input, filepath.Join(dir, "bad.json"), "DescriptionVector", "HotelId", 3); err == nil {
		t.Fatal("wrong dimensions accepted")
	}
}
