package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/Azure-DocumentDB/vector-search-go/internal/documentdb"
)

// This optional billable utility never opens a database connection and refuses
// to overwrite its input or an existing output. Explicit opt-in is mandatory.
func run() error {
	if os.Getenv("ALLOW_EMBEDDING_GENERATION") != "true" {
		return fmt.Errorf("billable generation disabled: set ALLOW_EMBEDDING_GENERATION=true explicitly")
	}
	batchSize, err := strconv.Atoi(documentdb.GetEnvOrDefault("EMBEDDING_SIZE_BATCH", "16"))
	if err != nil || batchSize < 1 || batchSize > 100 {
		return fmt.Errorf("EMBEDDING_SIZE_BATCH must be in [1,100]")
	}
	dimensions, err := strconv.Atoi(documentdb.GetEnvOrDefault("EMBEDDING_DIMENSIONS", "1536"))
	if err != nil || dimensions < 1 || dimensions > 2000 {
		return fmt.Errorf("EMBEDDING_DIMENSIONS must be in [1,2000]")
	}
	timeout, err := time.ParseDuration(documentdb.GetEnvOrDefault("VECTOR_SEARCH_TIMEOUT", "5m"))
	if err != nil || timeout < time.Second || timeout > 30*time.Minute {
		return fmt.Errorf("VECTOR_SEARCH_TIMEOUT must be between 1s and 30m")
	}
	input := documentdb.GetEnvOrDefault("DATA_FILE_WITHOUT_VECTORS", "data/Hotels.json")
	output := documentdb.GetEnvOrDefault("DATA_FILE_WITH_VECTORS", "data/Hotels_Vector.json")
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return fmt.Errorf("output already exists or cannot be checked; choose a new DATA_FILE_WITH_VECTORS")
	}
	data, err := documentdb.ReadFileReturnJSON(input)
	if err != nil {
		return err
	}
	field := documentdb.GetEnvOrDefault("FIELD_TO_EMBED", "Description")
	vectorField := documentdb.GetEnvOrDefault("EMBEDDED_FIELD", "DescriptionVector")
	texts := make([]string, len(data))
	for i, doc := range data {
		text, ok := doc[field].(string)
		if !ok || text == "" {
			return fmt.Errorf("document %d has no nonempty embedding text", i)
		}
		if _, exists := doc[vectorField]; exists {
			return fmt.Errorf("document %d already contains the output vector field", i)
		}
		texts[i] = text
	}
	c := documentdb.Config{EmbeddingMode: "azure-openai", OpenAIEndpoint: os.Getenv("AZURE_OPENAI_EMBEDDING_ENDPOINT"), OpenAIKey: os.Getenv("AZURE_OPENAI_EMBEDDING_KEY"), ModelName: os.Getenv("AZURE_OPENAI_EMBEDDING_MODEL")}
	client, err := documentdb.NewEmbeddingClient(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for start := 0; start < len(data); start += batchSize {
		end := min(start+batchSize, len(data))
		vectors, err := documentdb.GenerateEmbeddings(ctx, client, texts[start:end], c.ModelName, dimensions)
		if err != nil {
			return err
		}
		for i, vector := range vectors {
			data[start+i][vectorField] = vector
		}
		if end < len(data) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if err := documentdb.WriteFileJSON(data, output); err != nil {
		return err
	}
	fmt.Printf("Generated embeddings for %d documents (%d dimensions); wrote a new file. No MongoDB operations.\n", len(data), dimensions)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
