package documentdb

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestVectorValidation(t *testing.T) {
	for _, vector := range [][]float64{nil, {1}, {0, 0}, {math.NaN(), 1}, {math.Inf(1), 1}} {
		if err := ValidateVector(vector, 2); err == nil {
			t.Fatalf("invalid vector accepted: %v", vector)
		}
	}
	if err := ValidateVector([]float64{1, -0.25}, 2); err != nil {
		t.Fatal(err)
	}
	for _, value := range []interface{}{[]interface{}{nil, 1.0}, []interface{}{"1", 1.0}, "vector", nil} {
		if _, err := numericVector(value, 2); err == nil {
			t.Fatal("invalid numeric array accepted")
		}
	}
}

func TestPrepareSeedDeterministicAndNonmutating(t *testing.T) {
	c := configFixture(t)
	c.Dimensions = 2
	input := []map[string]interface{}{{"HotelId": "1", "DescriptionVector": []float64{1, 0}, "extra": "preserve"}}
	docs, err := PrepareSeed(input, c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PrepareSeed(input, c)
	if err != nil || !reflect.DeepEqual(docs, again) {
		t.Fatal("seed not deterministic")
	}
	if _, exists := input[0]["_id"]; exists {
		t.Fatal("input mutated")
	}
	if docs[0]["_id"] != "vectorsearch:1" || docs[0]["extra"] != "preserve" {
		t.Fatal("ID/fields lost")
	}
	input[0]["_id"] = "explicit"
	docs, err = PrepareSeed(input, c)
	if err != nil || docs[0]["_id"] != "explicit" {
		t.Fatal("explicit ID lost")
	}
}

func TestPrepareSeedRejectsWholeInvalidInput(t *testing.T) {
	c := configFixture(t)
	c.Dimensions = 2
	valid := map[string]interface{}{"HotelId": "1", "DescriptionVector": []float64{1, 0}}
	for _, bad := range []map[string]interface{}{
		nil, {"DescriptionVector": []float64{1, 0}}, {"HotelId": 3, "DescriptionVector": []float64{1, 0}}, {"HotelId": "2", "DescriptionVector": []float64{1}}, {"_id": 42, "DescriptionVector": []float64{1, 0}}, {"HotelId": "2", "DescriptionVector": []float64{1, 0}, "$bad": true}, valid,
	} {
		if _, err := PrepareSeed([]map[string]interface{}{valid, bad}, c); err == nil {
			t.Fatal("invalid second document did not reject entire seed")
		}
	}
	if _, err := PrepareSeed(nil, c); err == nil {
		t.Fatal("empty seed accepted")
	}
}

type memoryWriter struct {
	docs       map[string]bson.M
	calls      int
	err        error
	incomplete bool
}

func (m *memoryWriter) BulkWrite(ctx context.Context, models []mongo.WriteModel, opts ...*options.BulkWriteOptions) (*mongo.BulkWriteResult, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if len(opts) != 1 || opts[0].Ordered == nil || !*opts[0].Ordered {
		panic("unordered seed")
	}
	result := &mongo.BulkWriteResult{}
	for _, model := range models {
		update, ok := model.(*mongo.UpdateOneModel)
		if !ok {
			panic("seed must use UpdateOne")
		}
		filter := update.Filter.(bson.M)
		if len(filter) != 1 {
			panic("unscoped update")
		}
		id := filter["_id"].(string)
		operation := update.Update.(bson.M)
		if len(operation) != 1 || update.Upsert == nil || !*update.Upsert {
			panic("not insert-only upsert")
		}
		doc, ok := operation["$setOnInsert"].(bson.M)
		if !ok {
			panic("replacement update")
		}
		if _, exists := m.docs[id]; exists {
			result.MatchedCount++
		} else {
			m.docs[id] = doc
			result.UpsertedCount++
		}
	}
	if m.incomplete {
		return &mongo.BulkWriteResult{}, nil
	}
	return result, nil
}

func TestSeedIdempotenceAndPreservation(t *testing.T) {
	writer := &memoryWriter{docs: map[string]bson.M{"1": {"_id": "1", "owner": "existing"}, "unrelated": {"_id": "unrelated"}}}
	docs := []bson.M{{"_id": "1", "owner": "new"}, {"_id": "2", "owner": "sample"}}
	first, err := SeedData(context.Background(), writer, docs, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SeedData(context.Background(), writer, docs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Inserted != 1 || first.Existing != 1 || second.Inserted != 0 || second.Existing != 2 || len(writer.docs) != 3 || writer.docs["1"]["owner"] != "existing" {
		t.Fatal("seed replaced, deleted, or duplicated existing data")
	}
}

func TestSeedFailsClosed(t *testing.T) {
	docs := []bson.M{{"_id": "1"}}
	for _, size := range []int{0, -1, 1001} {
		writer := &memoryWriter{}
		if _, err := SeedData(context.Background(), writer, docs, size); err == nil || writer.calls != 0 {
			t.Fatal("bad batch size reached writer")
		}
	}
	writer := &memoryWriter{}
	if _, err := SeedData(context.Background(), writer, []bson.M{{"_id": "1"}, {"oops": 2}}, 1); err == nil || writer.calls != 0 {
		t.Fatal("bad IDs reached writer")
	}
	cause := errors.New("private document")
	writer = &memoryWriter{err: cause}
	if _, err := SeedData(context.Background(), writer, docs, 1); err == nil || !errors.Is(err, cause) || strings.Contains(err.Error(), cause.Error()) {
		t.Fatal("write failure lost or leaked")
	}
	writer = &memoryWriter{docs: map[string]bson.M{}, incomplete: true}
	if _, err := SeedData(context.Background(), writer, docs, 1); err == nil {
		t.Fatal("incomplete acknowledgement accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer = &memoryWriter{}
	if _, err := SeedData(ctx, writer, docs, 1); !errors.Is(err, context.Canceled) || writer.calls != 0 {
		t.Fatal("cancelled seed reached writer")
	}
}

func TestReadAndWriteFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if _, err := ReadFileReturnJSON(path); err == nil {
		t.Fatal("missing file accepted")
	}
	for _, raw := range []string{"invalid", "[]", "null"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFileReturnJSON(path); err == nil {
			t.Fatal("invalid data accepted")
		}
	}
	out := filepath.Join(dir, "output.json")
	data := []map[string]interface{}{{"HotelId": "1"}}
	if err := WriteFileJSON(data, out); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileJSON(nil, out); err == nil {
		t.Fatal("overwrote existing output")
	}
	got, err := ReadFileReturnJSON(out)
	if err != nil || !reflect.DeepEqual(got, data) {
		t.Fatal("output changed")
	}
	query := filepath.Join(dir, "query.json")
	if err := os.WriteFile(query, []byte("[1,0]"), 0600); err != nil {
		t.Fatal(err)
	}
	if v, err := ReadQueryVector(query, 2); err != nil || !reflect.DeepEqual(v, []float64{1, 0}) {
		t.Fatal("valid query failed")
	}
}

func TestRunValidatesBeforeNetwork(t *testing.T) {
	c := configFixture(t)
	c.AllowWrites = false
	if _, err := Run(context.Background(), c, "ivf"); err == nil {
		t.Fatal("run without opt-in succeeded")
	}
	c.AllowWrites = true
	c.DataFile = filepath.Join(t.TempDir(), "missing.json")
	if _, err := Run(context.Background(), c, "ivf"); err == nil || !strings.Contains(err.Error(), "read data file") {
		t.Fatal("missing input did not fail before connecting")
	}
}
