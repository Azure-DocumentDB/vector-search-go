package documentdb

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func indexInfo(t *testing.T, c Config, algorithm string) IndexInfo {
	t.Helper()
	spec, err := IndexSpec(c, algorithm)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bson.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var result IndexInfo
	if err := bson.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func indexDoc(t *testing.T, c Config, algorithm string) bson.D {
	t.Helper()
	d, err := IndexSpec(c, algorithm)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIndexContracts(t *testing.T) {
	c := configFixture(t)
	for _, algorithm := range []string{"ivf", "hnsw", "diskann"} {
		t.Run(algorithm, func(t *testing.T) {
			index := indexInfo(t, c, algorithm)
			if index.CosmosSearchOptions["kind"] != "vector-"+algorithm || index.CosmosSearchOptions["dimensions"] != int32(1536) {
				t.Fatal("wrong vector index options")
			}
			found, err := CompatibleIndex([]IndexInfo{index}, c, algorithm)
			if err != nil || !found {
				t.Fatal("compatible index not recognized")
			}
			index.Name = "different-name"
			found, err = CompatibleIndex([]IndexInfo{index}, c, algorithm)
			if err != nil || !found {
				t.Fatal("equivalent index unnecessarily duplicated")
			}
			index.CosmosSearchOptions["dimensions"] = int32(2)
			if _, err := CompatibleIndex([]IndexInfo{index}, c, algorithm); err == nil {
				t.Fatal("conflicting dimension accepted")
			}
		})
	}
}

func TestSearchPipelineContract(t *testing.T) {
	pipeline, err := SearchPipeline([]float64{1, 0}, "DescriptionVector", 2, 7)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bson.Marshal(bson.M{"pipeline": pipeline})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct{ Pipeline []bson.M }
	if err := bson.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	search := decoded.Pipeline[0]["$search"].(bson.M)
	cosmos := search["cosmosSearch"].(bson.M)
	if cosmos["k"] != int32(7) || cosmos["path"] != "DescriptionVector" || !reflect.DeepEqual(cosmos["vector"], bson.A{float64(1), float64(0)}) {
		t.Fatal("query options not passed through")
	}
	if _, err := SearchPipeline([]float64{1, 0}, "$bad", 2, 1); err == nil {
		t.Fatal("invalid field accepted")
	}
}

func TestMongoWorkflowMocks(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).CreateCollection(false))
	for _, algorithm := range []string{"ivf", "hnsw", "diskann"} {
		mt.Run(algorithm+"_creates_missing_index_before_seed", func(mt *mtest.T) {
			c := configFixture(mt.T)
			c.Dimensions = 2
			coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection(algorithm))
			ns := c.DatabaseName + "." + coll.Name()
			mt.AddMockResponses(
				mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "namespace missing"}), mtest.CreateSuccessResponse(),
				mtest.CreateCursorResponse(0, ns, mtest.FirstBatch, indexDoc(mt.T, c, algorithm)),
				mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 0}, bson.E{Key: "upserted", Value: bson.A{bson.D{{Key: "index", Value: 0}, {Key: "_id", Value: "1"}}}}),
				mtest.CreateCursorResponse(0, ns, mtest.FirstBatch, bson.D{{Key: "document", Value: bson.M{"_id": "1"}}, {Key: "score", Value: 1.0}}),
			)
			result, err := runOnCollection(context.Background(), coll, c, algorithm, []bson.M{{"_id": "1", "DescriptionVector": []float64{1, 0}}}, []float64{1, 0})
			if err != nil || result.Seed.Inserted != 1 || len(result.Results) != 1 {
				mt.Fatalf("workflow failed: %v %+v", err, result)
			}
			var names []string
			for _, e := range mt.GetAllStartedEvents() {
				names = append(names, e.CommandName)
				if e.DatabaseName != c.DatabaseName {
					mt.Fatal("wrong database")
				}
			}
			if !reflect.DeepEqual(names, []string{"listIndexes", "createIndexes", "listIndexes", "update", "aggregate"}) {
				mt.Fatalf("unsafe/unexpected commands: %v", names)
			}
		})
	}
	mt.Run("existing_index_checked_without_recreation", func(mt *mtest.T) {
		c := configFixture(mt.T)
		coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection("ivf"))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, c.DatabaseName+"."+coll.Name(), mtest.FirstBatch, indexDoc(mt.T, c, "ivf")))
		if err := EnsureVectorIndex(context.Background(), coll, c, "ivf"); err != nil {
			mt.Fatal(err)
		}
		events := mt.GetAllStartedEvents()
		if len(events) != 1 || events[0].CommandName != "listIndexes" {
			mt.Fatal("existing index was mutated")
		}
	})
	mt.Run("conflict_before_seed_or_mutation", func(mt *mtest.T) {
		c := configFixture(mt.T)
		coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection("ivf"))
		mt.AddMockResponses(mtest.CreateCursorResponse(0, c.DatabaseName+"."+coll.Name(), mtest.FirstBatch, indexDoc(mt.T, c, "hnsw")))
		_, err := runOnCollection(context.Background(), coll, c, "ivf", []bson.M{{"_id": "1"}}, make([]float64, 1536))
		if err == nil {
			mt.Fatal("conflict not rejected")
		}
		if len(mt.GetAllStartedEvents()) != 1 {
			mt.Fatal("mutation after index conflict")
		}
	})
	mt.Run("wrong_namespace_no_network", func(mt *mtest.T) {
		c := configFixture(mt.T)
		if err := EnsureVectorIndex(context.Background(), mt.Coll, c, "ivf"); err == nil {
			mt.Fatal("wrong namespace accepted")
		}
		if len(mt.GetAllStartedEvents()) != 0 {
			mt.Fatal("wrong namespace reached server")
		}
	})
	mt.Run("index_list_errors", func(mt *mtest.T) {
		c := configFixture(mt.T)
		coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection("ivf"))
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "secret"}))
		err := EnsureVectorIndex(context.Background(), coll, c, "ivf")
		var commandErr mongo.CommandError
		if err == nil || !errors.As(err, &commandErr) || commandErr.Code != 13 {
			mt.Fatal("list error swallowed")
		}
	})
	mt.Run("index_create_errors", func(mt *mtest.T) {
		c := configFixture(mt.T)
		coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection("ivf"))
		ns := c.DatabaseName + "." + coll.Name()
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch), mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 2, Message: "unsupported tier"}))
		if err := EnsureVectorIndex(context.Background(), coll, c, "ivf"); err == nil {
			mt.Fatal("create error swallowed")
		}
	})
	mt.Run("index_decode_errors", func(mt *mtest.T) {
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.coll", mtest.FirstBatch, bson.D{{Key: "name", Value: 12}}))
		if _, err := ListIndexes(context.Background(), mt.Coll); err == nil {
			mt.Fatal("decode error swallowed")
		}
	})
	mt.Run("search_decode_errors", func(mt *mtest.T) {
		c := configFixture(mt.T)
		c.Dimensions = 2
		mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.coll", mtest.FirstBatch, bson.D{{Key: "score", Value: "not numeric"}}))
		if _, err := Search(context.Background(), mt.Coll, c, []float64{1, 0}); err == nil {
			mt.Fatal("search decode error swallowed")
		}
	})
	mt.Run("search_cursor_errors", func(mt *mtest.T) {
		c := configFixture(mt.T)
		c.Dimensions = 2
		mt.AddMockResponses(mtest.CreateCursorResponse(123, "test.coll", mtest.FirstBatch), mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 43, Message: "cursor lost"}), mtest.CreateSuccessResponse())
		if _, err := Search(context.Background(), mt.Coll, c, []float64{1, 0}); err == nil {
			mt.Fatal("cursor error swallowed")
		}
	})
	mt.Run("empty_search_deadline", func(mt *mtest.T) {
		c := configFixture(mt.T)
		c.Dimensions = 2
		coll := mt.Client.Database(c.DatabaseName).Collection(c.Collection("ivf"))
		ns := c.DatabaseName + "." + coll.Name()
		mt.AddMockResponses(mtest.CreateCursorResponse(0, ns, mtest.FirstBatch, indexDoc(mt.T, c, "ivf")), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 0}), mtest.CreateCursorResponse(0, ns, mtest.FirstBatch))
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		if _, err := runOnCollection(ctx, coll, c, "ivf", []bson.M{{"_id": "1"}}, []float64{1, 0}); !errors.Is(err, context.DeadlineExceeded) {
			mt.Fatalf("empty results should time out, got %v", err)
		}
	})
}

func TestSearchResultPresenceAndScores(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock).CreateCollection(false))
	cases := []struct {
		name  string
		doc   bson.D
		valid bool
	}{
		{"missing_score", bson.D{{Key: "document", Value: bson.M{"_id": "1"}}}, false},
		{"null_score", bson.D{{Key: "document", Value: bson.M{"_id": "1"}}, {Key: "score", Value: nil}}, false},
		{"missing_id", bson.D{{Key: "document", Value: bson.M{"name": "hotel"}}, {Key: "score", Value: 1.0}}, false},
		{"null_id", bson.D{{Key: "document", Value: bson.M{"_id": nil}}, {Key: "score", Value: 1.0}}, false},
		{"zero_score", bson.D{{Key: "document", Value: bson.M{"_id": "1"}}, {Key: "score", Value: 0.0}}, true},
		{"negative_score", bson.D{{Key: "document", Value: bson.M{"_id": "1"}}, {Key: "score", Value: -0.5}}, true},
		{"integer_zero_score", bson.D{{Key: "document", Value: bson.M{"_id": "1"}}, {Key: "score", Value: int32(0)}}, true},
	}
	for _, tc := range cases {
		mt.Run(tc.name, func(mt *mtest.T) {
			c := configFixture(mt.T)
			c.Dimensions = 2
			mt.AddMockResponses(mtest.CreateCursorResponse(0, "test.coll", mtest.FirstBatch, tc.doc))
			result, err := Search(context.Background(), mt.Coll, c, []float64{1, 0})
			if tc.valid {
				if err != nil || len(result) != 1 {
					mt.Fatalf("legitimate cosine result rejected: %v", err)
				}
			} else if err == nil {
				mt.Fatal("malformed result accepted")
			}
		})
	}
}
