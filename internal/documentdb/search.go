package documentdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type IndexInfo struct {
	Name                string `bson:"name" json:"name"`
	Key                 bson.M `bson:"key" json:"key"`
	CosmosSearchOptions bson.M `bson:"cosmosSearchOptions" json:"cosmosSearchOptions,omitempty"`
}

func IndexSpec(c Config, algorithm string) (bson.D, error) {
	if err := c.ValidateWriteScope(algorithm); err != nil {
		return nil, err
	}
	settings := bson.D{{Key: "kind", Value: "vector-" + algorithm}, {Key: "dimensions", Value: c.Dimensions}, {Key: "similarity", Value: "COS"}}
	switch algorithm {
	case "ivf":
		settings = append(settings, bson.E{Key: "numLists", Value: 1}) // Deliberately small sample, not a tuned benchmark.
	case "hnsw":
		settings = append(settings, bson.E{Key: "m", Value: 16}, bson.E{Key: "efConstruction", Value: 64})
	case "diskann":
		settings = append(settings, bson.E{Key: "maxDegree", Value: 20}, bson.E{Key: "lBuild", Value: 50})
	}
	return bson.D{{Key: "name", Value: algorithm + "_index_" + c.VectorField}, {Key: "key", Value: bson.D{{Key: c.VectorField, Value: "cosmosSearch"}}}, {Key: "cosmosSearchOptions", Value: settings}}, nil
}

func ListIndexes(ctx context.Context, collection *mongo.Collection) ([]IndexInfo, error) {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		var commandErr mongo.CommandError
		if errors.As(err, &commandErr) && commandErr.Code == 26 {
			return nil, nil
		} // Namespace not yet created.
		return nil, protect("list indexes", err)
	}
	defer cursor.Close(ctx)
	var indexes []IndexInfo
	for cursor.Next(ctx) {
		var index IndexInfo
		if err := cursor.Decode(&index); err != nil {
			return nil, protect("decode indexes", err)
		}
		indexes = append(indexes, index)
	}
	if err := cursor.Err(); err != nil {
		return nil, protect("read indexes cursor", err)
	}
	return indexes, nil
}

// CompatibleIndex rejects a conflict rather than dropping/replacing any index.
// Every run checks the catalog, independently of whether seed data exists.
func CompatibleIndex(indexes []IndexInfo, c Config, algorithm string) (bool, error) {
	spec, err := IndexSpec(c, algorithm)
	if err != nil {
		return false, err
	}
	expectedName := spec[0].Value.(string)
	settings := spec[2].Value.(bson.D)
	found := false
	for _, index := range indexes {
		_, sameField := index.Key[c.VectorField]
		if index.Name != expectedName && !(sameField && (index.Key[c.VectorField] == "cosmosSearch" || len(index.CosmosSearchOptions) > 0)) {
			continue
		}
		if len(index.Key) != 1 || index.Key[c.VectorField] != "cosmosSearch" {
			return false, errors.New("existing index name conflicts with requested vector index; use a new collection")
		}
		for _, setting := range settings {
			// BSON numeric values can be int32/int64/double; compare canonical values.
			if fmt.Sprint(index.CosmosSearchOptions[setting.Key]) != fmt.Sprint(setting.Value) {
				return false, fmt.Errorf("existing vector index has incompatible %s; use a new collection (no indexes were dropped)", setting.Key)
			}
		}
		found = true
	}
	return found, nil
}

func EnsureVectorIndex(ctx context.Context, collection *mongo.Collection, c Config, algorithm string) error {
	if err := c.ValidateWriteScope(algorithm); err != nil {
		return err
	}
	if collection.Database().Name() != c.DatabaseName || collection.Name() != c.Collection(algorithm) {
		return errors.New("collection does not match the authorized namespace")
	}
	indexes, err := ListIndexes(ctx, collection)
	if err != nil {
		return err
	}
	found, err := CompatibleIndex(indexes, c, algorithm)
	if err != nil || found {
		return err
	}
	spec, err := IndexSpec(c, algorithm)
	if err != nil {
		return err
	}
	command := bson.D{{Key: "createIndexes", Value: collection.Name()}, {Key: "indexes", Value: []bson.D{spec}}}
	if err := collection.Database().RunCommand(ctx, command).Err(); err != nil {
		return protect("create vector index (check service tier and permissions)", err)
	}
	// Do not report success on an acknowledged command without verifying catalog.
	indexes, err = ListIndexes(ctx, collection)
	if err != nil {
		return err
	}
	found, err = CompatibleIndex(indexes, c, algorithm)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("vector index was not visible after creation; retry the command, no cleanup is performed")
	}
	return nil
}

func SearchPipeline(vector []float64, field string, dimensions, topK int) (mongo.Pipeline, error) {
	if err := ValidateVector(vector, dimensions); err != nil {
		return nil, err
	}
	if !namePattern.MatchString(field) || topK < 1 || topK > 1000 {
		return nil, errors.New("invalid search field or top K")
	}
	return mongo.Pipeline{
		bson.D{{Key: "$search", Value: bson.D{{Key: "cosmosSearch", Value: bson.D{{Key: "vector", Value: vector}, {Key: "path", Value: field}, {Key: "k", Value: topK}}}, {Key: "returnStoredSource", Value: true}}}},
		bson.D{{Key: "$project", Value: bson.D{{Key: "document", Value: "$$ROOT"}, {Key: "score", Value: bson.D{{Key: "$meta", Value: "searchScore"}}}}}},
	}, nil
}

func Search(ctx context.Context, collection *mongo.Collection, c Config, vector []float64) ([]SearchResult, error) {
	pipeline, err := SearchPipeline(vector, c.VectorField, c.Dimensions, c.TopK)
	if err != nil {
		return nil, err
	}
	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, protect("vector search", err)
	}
	defer cursor.Close(ctx)
	var results []SearchResult
	for cursor.Next(ctx) {
		var result SearchResult
		if err := cursor.Decode(&result); err != nil {
			return nil, protect("decode vector results", err)
		}
		results = append(results, result)
	}
	if err := cursor.Err(); err != nil {
		return nil, protect("read vector results cursor", err)
	}
	if len(results) > c.TopK {
		return nil, errors.New("vector search returned more than top K")
	}
	for _, result := range results {
		if len(result.Document) == 0 || math.IsNaN(result.Score) || math.IsInf(result.Score, 0) {
			return nil, errors.New("invalid vector search result")
		}
	}
	return results, nil
}

type RunResult struct {
	Algorithm  string         `json:"algorithm"`
	Database   string         `json:"database"`
	Collection string         `json:"collection"`
	Mode       string         `json:"mode"`
	Seed       InsertStats    `json:"seed"`
	Results    []SearchResult `json:"results"`
}

// Run executes a bounded workflow. Callers must explicitly authorize writes.
// All local input/query validation happens before creating an index or seeding.
func Run(ctx context.Context, c Config, algorithm string) (report RunResult, err error) {
	if err = c.ValidateWriteScope(algorithm); err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	data, err := ReadFileReturnJSON(c.DataFile)
	if err != nil {
		return report, err
	}
	docs, err := PrepareSeed(data, c)
	if err != nil {
		return report, err
	}
	query, err := QueryVector(ctx, c)
	if err != nil {
		return report, err
	}
	client, err := ConnectMongo(ctx, c)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, Disconnect(client)) }()
	collection := client.Database(c.DatabaseName).Collection(c.Collection(algorithm))
	return runOnCollection(ctx, collection, c, algorithm, docs, query)
}

func runOnCollection(ctx context.Context, collection *mongo.Collection, c Config, algorithm string, docs []bson.M, query []float64) (RunResult, error) {
	report := RunResult{Algorithm: algorithm, Database: c.DatabaseName, Collection: c.Collection(algorithm), Mode: c.EmbeddingMode}
	if err := EnsureVectorIndex(ctx, collection, c, algorithm); err != nil {
		return report, err
	}
	stats, err := SeedData(ctx, collection, docs, c.BatchSize)
	report.Seed = stats
	if err != nil {
		return report, err
	}
	// Index visibility can lag inserts. Retry ONLY empty successful searches, not
	// auth, unsupported algorithm, server, or decoding errors. Context bounds wait.
	for {
		results, err := Search(ctx, collection, c, query)
		if err != nil {
			return report, err
		}
		if len(results) > 0 {
			report.Results = results
			return report, nil
		}
		select {
		case <-ctx.Done():
			return report, fmt.Errorf("no vector results before deadline: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}
