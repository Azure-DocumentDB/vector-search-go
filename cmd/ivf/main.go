package main

import (
	"context"
	"github.com/Azure-DocumentDB/vector-search-go/internal/documentdb"
	"log"
	"os"
)

func main() {
	if err := documentdb.RunCommand(context.Background(), os.Stdout, "ivf"); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
