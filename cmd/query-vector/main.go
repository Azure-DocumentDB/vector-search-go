// query-vector extracts one existing precomputed vector. It never calls a model
// or database. This produces a non-semantic self-match fixture, not a benchmark.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Azure-DocumentDB/vector-search-go/internal/documentdb"
)

func run(input, output, field, idField string, dimensions int) error {
	data, err := documentdb.ReadFileReturnJSON(input)
	if err != nil {
		return err
	}
	docs, err := documentdb.PrepareSeed(data, documentdb.Config{VectorField: field, SeedIDField: idField, Dimensions: dimensions})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(docs[0][field])
	if err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create query vector output; choose a new writable file")
	}
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("query vector output write failed")
	}
	fmt.Println("Extracted first document's stored vector. NON-SEMANTIC self-match fixture; no model or database calls.")
	return nil
}

func main() {
	input := flag.String("input", "data/Hotels_Vector.json", "precomputed document array")
	output := flag.String("output", "data/query-vector.json", "new output JSON numeric array (never overwritten)")
	field := flag.String("field", "DescriptionVector", "stored vector field")
	idField := flag.String("id-field", "HotelId", "deterministic seed ID field")
	dimensions := flag.Int("dimensions", 1536, "expected vector dimensions")
	flag.Parse()
	if err := run(*input, *output, *field, *idField, *dimensions); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
