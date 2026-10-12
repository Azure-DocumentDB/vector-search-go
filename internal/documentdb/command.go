package documentdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// LoadDatabaseConfig does not require or initialize an embedding deployment.
func LoadDatabaseConfig() (Config, error) {
	return loadConfig(func(key string) string {
		switch key {
		case "EMBEDDING_MODE":
			return "offline"
		case "QUERY_VECTOR_FILE":
			return "unused-for-index-inspection"
		default:
			return os.Getenv(key)
		}
	})
}

func RunCommand(ctx context.Context, out io.Writer, algorithm string) error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	report, err := Run(ctx, c, algorithm)
	if err != nil {
		return err
	}
	if c.EmbeddingMode == "offline" {
		if _, err := fmt.Fprintln(out, "OFFLINE VECTOR CONTRACT FIXTURE: no model calls; NOT a semantic relevance test or benchmark."); err != nil {
			return err
		}
	}
	// Summaries deliberately omit full documents, vectors, connection URIs, and tokens.
	type hit struct {
		ID    interface{} `json:"id"`
		Score float64     `json:"score"`
	}
	hits := make([]hit, 0, len(report.Results))
	for _, result := range report.Results {
		hits = append(hits, hit{ID: result.Document["_id"], Score: result.Score})
	}
	return json.NewEncoder(out).Encode(struct {
		Algorithm  string      `json:"algorithm"`
		Database   string      `json:"database"`
		Collection string      `json:"collection"`
		Mode       string      `json:"mode"`
		Seed       InsertStats `json:"seed"`
		Results    []hit       `json:"results"`
	}{report.Algorithm, report.Database, report.Collection, report.Mode, report.Seed, hits})
}
