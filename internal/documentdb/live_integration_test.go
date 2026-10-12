//go:build integration

package documentdb

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// These tests MUTATE the explicitly authorized disposable namespace. They never
// delete documents/indexes/resources. Cleanup belongs to the operator. A tagged
// invocation without the gate fails, rather than silently skipping/going green.
func TestLiveAlgorithms(t *testing.T) {
	if os.Getenv("VECTOR_SEARCH_LIVE_TEST") != "1" {
		t.Fatal("live integration requires VECTOR_SEARCH_LIVE_TEST=1; no network calls made")
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.EmbeddingMode != "offline" {
		t.Fatal("this gate proves precomputed vector contracts only; set EMBEDDING_MODE=offline")
	}
	if c.CollectionName != "" {
		t.Fatal("unset MONGO_COLLECTION for the three-algorithm gate; use MONGO_COLLECTION_PREFIX")
	}
	if err := c.ValidateWriteScope("ivf"); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFileReturnJSON(c.DataFile)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := PrepareSeed(data, c)
	if err != nil {
		t.Fatal(err)
	}
	query, err := QueryVector(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// The gate requires a known stored query fixture, not invented relevance.
	expectedIDs := map[string]bool{}
	for _, doc := range docs {
		if reflect.DeepEqual(doc[c.VectorField], query) {
			expectedIDs[doc["_id"].(string)] = true
		}
	}
	if len(expectedIDs) == 0 {
		t.Fatal("live fixture query must exactly match a stored seed vector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	client, err := ConnectMongo(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := Disconnect(client); err != nil {
			t.Error(err)
		}
	}()
	t.Logf("REAL AZURE precomputed vector contract; auth=%s dimensions=%d documents=%d (NOT semantic/model/benchmark proof)", c.AuthMode, c.Dimensions, len(docs))
	for _, algorithm := range []string{"ivf", "hnsw", "diskann"} {
		t.Run(algorithm, func(t *testing.T) {
			coll := client.Database(c.DatabaseName).Collection(c.Collection(algorithm))
			before := snapshot(t, ctx, coll)
			// Establish a populated collection before Run, proving index presence is not
			// conditional on an empty collection. This upsert cannot replace a document.
			if _, err := SeedData(ctx, coll, docs[:1], c.BatchSize); err != nil {
				t.Fatal(err)
			}
			first, err := runOnCollection(ctx, coll, c, algorithm, docs, query)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Results) != min(c.TopK, len(docs)) {
				t.Fatalf("wanted %d results, got %d", min(c.TopK, len(docs)), len(first.Results))
			}
			foundSelf := false
			for _, result := range first.Results {
				id, ok := result.Document["_id"].(string)
				if ok && expectedIDs[id] && result.Score >= 0.999 {
					foundSelf = true
				}
			}
			if !foundSelf {
				t.Fatal("known stored query vector did not return its source document with near-unit cosine score")
			}
			afterFirst := snapshot(t, ctx, coll)
			for id, raw := range before {
				if !reflect.DeepEqual(afterFirst[id], raw) {
					t.Fatal("an existing document changed or disappeared")
				}
			}
			second, err := runOnCollection(ctx, coll, c, algorithm, docs, query)
			if err != nil {
				t.Fatal(err)
			}
			if second.Seed.Inserted != 0 || second.Seed.Existing != int64(len(docs)) {
				t.Fatal("second seed was not idempotent")
			}
			afterSecond := snapshot(t, ctx, coll)
			if !reflect.DeepEqual(afterFirst, afterSecond) {
				t.Fatal("repeat run changed existing documents")
			}
			indexes, err := ListIndexes(ctx, coll)
			if err != nil {
				t.Fatal(err)
			}
			found, err := CompatibleIndex(indexes, c, algorithm)
			if err != nil || !found {
				t.Fatal("expected vector index not verified")
			}
			t.Logf("%s: real search=%d, index verified, repeat inserted=0 existing=%d, existing documents preserved", algorithm, len(second.Results), second.Seed.Existing)
		})
	}
}

func snapshot(t *testing.T, ctx context.Context, coll *mongo.Collection) map[string][]byte {
	t.Helper()
	cursor, err := coll.Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(protect("read live preservation snapshot", err))
	}
	defer cursor.Close(ctx)
	result := map[string][]byte{}
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			t.Fatal(protect("decode live preservation snapshot", err))
		}
		id, err := json.Marshal(doc["_id"])
		if err != nil {
			t.Fatal("invalid snapshot ID")
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal("invalid snapshot document")
		}
		result[string(id)] = raw
	}
	if err := cursor.Err(); err != nil {
		t.Fatal(protect("live preservation cursor", err))
	}
	return result
}
