package documentdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/openai/openai-go/v3"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type SearchResult struct {
	Document bson.M  `bson:"document" json:"document"`
	Score    float64 `bson:"score" json:"score"`
}

type InsertStats struct {
	Total    int   `json:"total"`
	Inserted int64 `json:"inserted"`
	Existing int64 `json:"existing"`
}

func ReadFileReturnJSON(path string) ([]map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, protect("read data file", err)
	}
	var data []map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, protect("decode data file", err)
	}
	if len(data) == 0 {
		return nil, errors.New("data file must contain a nonempty JSON array of documents")
	}
	return data, nil
}

// WriteFileJSON refuses to replace an existing file, including its input file.
func WriteFileJSON(data []map[string]interface{}, path string) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return protect("encode output", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return protect("create new output file", err)
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return protect("write output", err)
	}
	return nil
}

func ValidateVector(vector []float64, dimensions int) error {
	if dimensions < 1 || len(vector) != dimensions {
		return fmt.Errorf("vector dimension mismatch: expected %d, got %d", dimensions, len(vector))
	}
	nonzero := false
	for _, n := range vector {
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return errors.New("vector must contain finite numbers")
		}
		if n != 0 {
			nonzero = true
		}
	}
	if !nonzero {
		return errors.New("cosine vector must be nonzero")
	}
	return nil
}

func numericVector(value interface{}, dimensions int) ([]float64, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("invalid vector")
	}
	var vector []float64
	// Null elements must not silently turn into zero coordinates.
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, errors.New("vector must be a numeric array")
	}
	for _, e := range elements {
		if string(e) == "null" {
			return nil, errors.New("vector must not contain null")
		}
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		return nil, errors.New("vector must be a numeric array")
	}
	return vector, ValidateVector(vector, dimensions)
}

// PrepareSeed validates the entire input before any network call/mutation.
// Existing _id values or deterministic HotelId-derived IDs enable insert-only
// upserts. No synthetic random IDs, replacement, deletion, or data truncation.
func PrepareSeed(data []map[string]interface{}, c Config) ([]bson.M, error) {
	if len(data) == 0 {
		return nil, errors.New("seed must not be empty")
	}
	docs := make([]bson.M, 0, len(data))
	seen := map[string]bool{}
	for i, source := range data {
		vector, err := numericVector(source[c.VectorField], c.Dimensions)
		if err != nil {
			return nil, fmt.Errorf("seed document %d: %w", i, err)
		}
		id, hasID := source["_id"]
		if !hasID {
			s, ok := source[c.SeedIDField].(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("seed document %d requires a nonempty string _id or %s", i, c.SeedIDField)
			}
			id = "vectorsearch:" + s
		}
		idString, ok := id.(string)
		if !ok || idString == "" {
			return nil, fmt.Errorf("seed document %d _id must be a nonempty string", i)
		}
		if seen[idString] {
			return nil, fmt.Errorf("duplicate seed ID at document %d", i)
		}
		seen[idString] = true
		doc := bson.M{}
		for key, value := range source {
			if key == "" || key[0] == '$' || containsUnsafeKey(key) {
				return nil, fmt.Errorf("invalid seed field at document %d", i)
			}
			doc[key] = value
		}
		doc["_id"] = idString
		doc[c.VectorField] = vector
		docs = append(docs, doc)
	}
	return docs, nil
}

func containsUnsafeKey(key string) bool {
	for _, r := range key {
		if r == '.' || r == 0 {
			return true
		}
	}
	return false
}

type bulkWriter interface {
	BulkWrite(context.Context, []mongo.WriteModel, ...*options.BulkWriteOptions) (*mongo.BulkWriteResult, error)
}

// SeedData preserves every existing document, even when its source has changed.
// A failure may leave earlier upserts committed; retry is safe and idempotent.
func SeedData(ctx context.Context, collection bulkWriter, docs []bson.M, batchSize int) (InsertStats, error) {
	stats := InsertStats{Total: len(docs)}
	if batchSize < 1 || batchSize > 1000 || len(docs) == 0 {
		return stats, errors.New("seed requires documents and batch size in [1,1000]")
	}
	for _, doc := range docs {
		if id, ok := doc["_id"].(string); !ok || id == "" {
			return stats, errors.New("seed requires validated string IDs")
		}
	}
	for start := 0; start < len(docs); start += batchSize {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		end := min(start+batchSize, len(docs))
		models := make([]mongo.WriteModel, 0, end-start)
		for _, doc := range docs[start:end] {
			models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": doc["_id"]}).SetUpdate(bson.M{"$setOnInsert": doc}).SetUpsert(true))
		}
		result, err := collection.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(true))
		if result != nil {
			stats.Inserted += result.UpsertedCount
			stats.Existing += result.MatchedCount
		}
		if err != nil {
			return stats, protect("seed batch", err)
		}
		if result == nil || result.UpsertedCount+result.MatchedCount != int64(len(models)) {
			return stats, errors.New("seed batch returned an incomplete acknowledgement")
		}
	}
	return stats, nil
}

func ReadQueryVector(path string, dimensions int) ([]float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, protect("read query vector", err)
	}
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, protect("decode query vector", err)
	}
	return numericVector(value, dimensions)
}

func GenerateEmbeddings(ctx context.Context, client openai.Client, texts []string, model string, dimensions int) ([][]float64, error) {
	if model == "" || dimensions < 1 || dimensions > 2000 {
		return nil, errors.New("embedding deployment and dimensions in [1,2000] are required")
	}
	if len(texts) == 0 {
		return nil, errors.New("no texts to embed")
	}
	for _, text := range texts {
		if text == "" {
			return nil, errors.New("embedding text must not be empty")
		}
	}
	response, err := client.Embeddings.New(ctx, openai.EmbeddingNewParams{Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts}, Model: model, Dimensions: openai.Int(int64(dimensions))})
	if err != nil {
		return nil, protect("Azure OpenAI embedding request", err)
	}
	if len(response.Data) != len(texts) {
		return nil, errors.New("embedding response count mismatch")
	}
	result := make([][]float64, len(texts))
	for _, item := range response.Data {
		i := int(item.Index)
		if i < 0 || i >= len(result) || result[i] != nil {
			return nil, errors.New("invalid or duplicate embedding response index")
		}
		if err := ValidateVector(item.Embedding, dimensions); err != nil {
			return nil, err
		}
		result[i] = item.Embedding
	}
	return result, nil
}

func QueryVector(ctx context.Context, c Config) ([]float64, error) {
	if c.EmbeddingMode == "offline" {
		return ReadQueryVector(c.QueryVectorFile, c.Dimensions)
	}
	client, err := NewEmbeddingClient(c)
	if err != nil {
		return nil, err
	}
	vectors, err := GenerateEmbeddings(ctx, client, []string{c.QueryText}, c.ModelName, c.Dimensions)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}
