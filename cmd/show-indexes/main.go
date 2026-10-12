package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Azure-DocumentDB/vector-search-go/internal/documentdb"
	"log"
	"os"
)

func run() (err error) {
	c, err := documentdb.LoadDatabaseConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	client, err := documentdb.ConnectMongo(ctx, c)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, documentdb.Disconnect(client)) }()
	names := []string{c.Collection("ivf"), c.Collection("hnsw"), c.Collection("diskann")}
	if c.CollectionName != "" {
		names = names[:1]
	}
	result := map[string][]documentdb.IndexInfo{}
	for _, name := range names {
		indexes, err := documentdb.ListIndexes(ctx, client.Database(c.DatabaseName).Collection(name))
		if err != nil {
			return err
		}
		result[name] = indexes
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
