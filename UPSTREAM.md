# Upstream provenance

- Source repository: https://github.com/Azure-Samples/documentdb-samples
- Exact source revision: [f5a2675bbf349737234d0391e3ebee24b260e06f](https://github.com/Azure-Samples/documentdb-samples/commit/f5a2675bbf349737234d0391e3ebee24b260e06f)
- Source directory: [ai/vector-search-go](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/vector-search-go)
- Extraction date: 2026-09-30
- License: original Microsoft MIT notice preserved verbatim in [LICENSE.md](LICENSE.md)
- History model: independently maintained source snapshot, not a GitHub fork or ownership transfer

## Standalone adaptations

Source is at repository root. The README is rewritten for that layout, and placeholder-only configuration/data setup replaces reliance on the parent checkout.
- Adapted source file: `.env.example`
- Adapted source file: `README.md`
- Adapted source file: `cmd/create-embeddings/main.go` (upstream `src/create_embeddings.go`)
- Adapted source file: `internal/documentdb/documentdb.go` (upstream `src/utils.go`)

### Go layout restructure (2026-10-01)

Upstream keeps five `package main` files plus a shared `utils.go` in one `src/`
directory, which is valid only when each program is compiled with an explicit file
list (`go run src/ivf.go src/utils.go`). In a standalone repository that layout makes
`go build ./...` and `go vet ./...` fail with `main redeclared in this block`, so
ordinary Go tooling and CI cannot inspect the repository.

The programs were moved to one command directory each, with the shared helpers in an
internal package:

- `src/ivf.go` to `cmd/ivf/main.go`
- `src/hnsw.go` to `cmd/hnsw/main.go`
- `src/diskann.go` to `cmd/diskann/main.go`
- `src/create_embeddings.go` to `cmd/create-embeddings/main.go`
- `src/show_indexes.go` to `cmd/show-indexes/main.go`
- `src/utils.go` to `internal/documentdb/documentdb.go` (package `documentdb`)

The only source change is reference qualification: helpers are now called through the
`documentdb` package, and the previously unexported `getEnvOrDefault` is exported as
`GetEnvOrDefault` because it is used from `cmd/create-embeddings`. No vector search
logic, index parameters, dependency versions, or algorithm behavior were modified.

`go build ./...` now exits 0 and all five commands compile. `go vet ./...` still
reports pre-existing `primitive.E struct literal uses unkeyed fields` warnings that are
inherited from upstream and intentionally left unchanged.

Hotel fixtures are not redistributed: [data/README.md](data/README.md) pins the original input locations and checksums. Saved output, Learn-only quickstart pages, and devcontainer automation are omitted. Shared infrastructure and root monorepo CI are not copied.

No dependency versions or vector/agent algorithms are intentionally changed. The Go agent module namespace and local data/environment-file paths are adjusted where necessary. Invalid monorepo-only npm entry points are removed rather than advertised as working commands.

## Maintenance

There is no automatic synchronization. Review upstream changes against this pinned baseline, apply only relevant fixes, retain notices, and record the imported revision and local adaptations here.
Issue/PR history is not transferred. The maintainers of the repository hosting this extraction own its downstream fixes; contribute reusable fixes upstream under its contribution policy.
