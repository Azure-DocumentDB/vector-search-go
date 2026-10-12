# vector-search-go

Go samples for **Azure DocumentDB (with MongoDB compatibility)**: seed precomputed hotel vectors and query IVF, HNSW, and DiskANN indexes. Azure's managed `$search` / `cosmosSearch` dialect is required; these are not examples for an arbitrary local MongoDB server.

Extracted from the [pinned Microsoft sample](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/vector-search-go), with downstream safety, authentication, and testing changes recorded in [UPSTREAM.md](UPSTREAM.md). Original [MIT license](LICENSE.md) retained.

## What the two modes prove

- **Precomputed vectors (`EMBEDDING_MODE=offline`)**: real database authentication, idempotent seeding, vector index creation/inspection, and nearest-neighbor queries. The query is an existing stored vector. **No model calls, no OpenAI resource, and no semantic relevance or benchmark claim.** “Offline” describes embedding generation only; the database is still a real, billable Azure service.
- **Azure OpenAI (`EMBEDDING_MODE=azure-openai`, default)**: embeds `QUERY_TEXT` using your actual deployed model, then searches the same database. Requires a compatible embedding deployment and permission to call it. Model availability, quota, authorization, and relevance must be validated independently; passing unit tests or the precomputed-vector gate does not prove them. There is no fallback to mock embeddings.

CI is local build/unit/mock validation only. It never deploys Azure resources or runs the live gate. No saved output is presented as live evidence.

## Prerequisites and safety

- Go **1.24.12 or newer**, Git, and Azure CLI or another [DefaultAzureCredential](https://learn.microsoft.com/azure/developer/go/sdk/authentication/credential-chains) sign-in mechanism for passwordless mode.
- An explicitly approved, **disposable** Azure DocumentDB cluster. Confirm current region/tier support and network access; HNSW/DiskANN availability depends on service tier. See [vector search documentation](https://learn.microsoft.com/azure/documentdb/vector-search).
- For OIDC, register the correct Microsoft Entra principal with appropriate database roles on the cluster. An Azure subscription role or `az login` alone is not database authorization. [Connection/RBAC guidance](https://learn.microsoft.com/azure/documentdb/how-to-connect-role-based-access-control).
- Explicit write permission: `VECTOR_SEARCH_ALLOW_WRITES=true`. Otherwise the search commands fail before any cloud calls.
- The actual target database **must** be `vectorsearch_disposable` or `vectorsearch_disposable_<lowercase_run_id>` (letters, digits, underscores; maximum 63 characters). `Hotels`, `admin`, and other arbitrary databases are rejected. Choose a unique run ID and least-privilege database roles; the name guard is not a substitute for authorization.
- All three commands use the configured database and collection. No deletion/reset/drop operation exists. Existing documents are never replaced, and existing indexes are never dropped. An incompatible vector index fails before seeding; select a new collection rather than resetting an old one.
- Every run checks index presence, even if documents already exist. Seeding uses `_id`-filtered `$setOnInsert` upserts. Supply unique, nonempty string `_id` values or string `HotelId` values (configurable `SEED_ID_FIELD`); derived IDs are `vectorsearch:<HotelId>`. Reusing an ID with changed source data intentionally **preserves the stored document**. Use a new collection to test a new dataset/model.
- Validate the entire seed and query before database mutations: nonempty input, unique IDs, finite nonzero cosine vectors, and exact dimensions. Failed batches may leave earlier upserts committed; rerunning is safe. No automatic cleanup is attempted, so operators remain responsible for the disposable database/resources and their costs.

## Quick start: real Azure, no model required

The examples below use a POSIX shell from the repository root. PowerShell users can set equivalent process variables with `$env:NAME = 'value'`.

### 1. Fetch and verify the data deliberately

Follow [data/README.md](data/README.md) for pinned URLs, SHA-256 verification, source terms, and PowerShell downloads. Data is not redistributed and is never fetched automatically.

```sh
go mod download
go run ./cmd/query-vector -input data/Hotels_Vector.json -output data/query-vector.json
```

`query-vector` validates the data and extracts the first document's stored 1536-dimensional vector into a JSON numeric array. It has **no database or model dependency**, refuses to overwrite an existing output, and labels its result a non-semantic self-match fixture. For other inputs use `-dimensions`, `-field`, and `-id-field`.

### 2. Configure an authorized disposable namespace

The programs read **only process environment variables**; they do not implicitly load `.env` files. [.env.example](.env.example) is a placeholder reference, not an executable script. Never commit a populated environment file or paste credentials into logs/command history.

```sh
export MONGO_AUTH_MODE=oidc
export MONGO_CLUSTER_NAME='<your-approved-cluster>'
export MONGO_DATABASE=vectorsearch_disposable_my_run
export MONGO_COLLECTION_PREFIX=hotels
export VECTOR_SEARCH_ALLOW_WRITES=true
export EMBEDDING_MODE=offline
export DATA_FILE_WITH_VECTORS="$PWD/data/Hotels_Vector.json"
export QUERY_VECTOR_FILE="$PWD/data/query-vector.json"
export EMBEDDING_DIMENSIONS=1536
export TOP_K=5
export VECTOR_SEARCH_TIMEOUT=5m
```

Sign in to the intended Azure tenant, verify the intended principal, and confirm its **database** role separately. Passwordless is the cloud default: `DefaultAzureCredential` requests the fixed public-Azure scope `https://ossrdbms-aad.database.windows.net/.default`; the MongoDB OIDC callback returns both the token and its expiry. It rejects empty/expired tokens and never derives a token audience from server-supplied input. `MONGO_HOST` can supply the exact `.mongocluster.cosmos.azure.com` or `.global.mongocluster.cosmos.azure.com` FQDN instead of a cluster name. Optional `MONGO_OIDC_USERNAME` is available when your principal setup requires a client ID. Sovereign-cloud domains/audiences are not supported by this sample.

Alternatively, set `MONGO_AUTH_MODE=connection-string` and inject `MONGO_CONNECTION_STRING` securely into the process environment. Use your approved native SCRAM `mongodb+srv://` URI, **without a database path**; `MONGO_DATABASE` determines the actual target. Never print the URI. TLS and certificate/hostname verification are mandatory in both modes; TLS bypass options are rejected. In OIDC mode, unset `MONGO_CONNECTION_STRING` to prevent ambiguous credential selection. Authentication is proven by a ping; there is no silent auth-mode fallback.

### 3. Run the three algorithms

```sh
go run ./cmd/ivf
go run ./cmd/hnsw
go run ./cmd/diskann
# Read-only inspection of only the configured sample collections:
go run ./cmd/show-indexes
```

Collections are `hotels_ivf`, `hotels_hnsw`, and `hotels_diskann` by default. Change `MONGO_COLLECTION_PREFIX` to isolate another run. `MONGO_COLLECTION` is an explicit single-command override; do not share one collection between incompatible algorithms. `show-indexes` needs no model configuration and does not enumerate other databases.

Each search prints a mode label and JSON summary containing its actual database/collection, inserted/existing counts, result IDs, and scores. It omits complete documents, vectors, URLs, and tokens. Zero new inserts on a repeated run are success, not failure. Errors exit nonzero; SDK/driver error details are withheld because they can contain credentials or document content. Whole-run deadlines bound connection, index, seed, model, and query operations. Only empty successful searches are retried while waiting for index visibility; actual errors are returned, not masked. An index not visible immediately after creation is an error and can be retried safely.

Index parameters are small-sample defaults, **not performance tuning**: IVF `numLists=1`; HNSW `m=16`, `efConstruction=64`; DiskANN `maxDegree=20`, `lBuild=50`; all use cosine similarity. The sample checks these settings when reusing an existing index.

## Optional: a real embedding deployment

Precomputed seed vectors and query embeddings must come from compatible models and dimensions. Do not merely choose a deployment with the same output length. For the pinned hotel data, confirm its source model in the [upstream data notes](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/data/README.md).

```sh
export EMBEDDING_MODE=azure-openai
unset QUERY_VECTOR_FILE
export AZURE_OPENAI_EMBEDDING_ENDPOINT='https://<your-resource>.openai.azure.com/'
export AZURE_OPENAI_EMBEDDING_MODEL='<your-embedding-deployment-name>'
export QUERY_TEXT='quintessential lodging near running trails, eateries, retail'
go run ./cmd/ivf
```

The endpoint is an HTTPS resource origin. Requests use `/openai/v1/embeddings`, the **deployment name**, and configured dimensions. Azure OpenAI uses `DefaultAzureCredential` by default, independently of database authentication. An optional `AZURE_OPENAI_EMBEDDING_KEY` can be injected securely; it is never needed in offline mode. There is no hardcoded deployment name or automatic resource/model provisioning.

To regenerate a dataset, first select a **new** output path and explicitly allow billable generation:

```sh
export ALLOW_EMBEDDING_GENERATION=true
export DATA_FILE_WITHOUT_VECTORS="$PWD/data/Hotels.json"
export DATA_FILE_WITH_VECTORS="$PWD/data/Hotels_Vector_new.json"
go run ./cmd/create-embeddings
```

This utility never connects to MongoDB. It validates all text before model calls, verifies response counts/indices/dimensions, respects cancellation, and refuses to overwrite a file or existing vector fields. `FIELD_TO_EMBED` defaults to `Description`, `EMBEDDING_SIZE_BATCH` to 16. Use the regenerated data in a new collection. If model provisioning, quota, or authentication is unavailable, this optional path remains unvalidated; do not describe a precomputed run as a semantic end-to-end success.

## Configuration reference

| Variable | Default / contract |
| --- | --- |
| `MONGO_AUTH_MODE` | `oidc` or explicit `connection-string` |
| `MONGO_CLUSTER_NAME` / `MONGO_HOST` | Required for OIDC; exact host takes precedence |
| `MONGO_OIDC_USERNAME` | Optional client ID for OIDC |
| `MONGO_CONNECTION_STRING` | Required only for native SCRAM mode; secret |
| `MONGO_DATABASE` | `vectorsearch_disposable`; enforced disposable prefix |
| `MONGO_COLLECTION_PREFIX` | `hotels` |
| `MONGO_COLLECTION` | Optional exact collection override for one command |
| `VECTOR_SEARCH_ALLOW_WRITES` | `false`; must be exactly `true` to mutate |
| `DATA_FILE_WITH_VECTORS` | `data/Hotels_Vector.json` |
| `SEED_ID_FIELD` | `HotelId` when `_id` is absent |
| `EMBEDDED_FIELD` | `DescriptionVector` (simple top-level identifier) |
| `EMBEDDING_MODE` | `azure-openai`; explicit `offline` avoids model calls |
| `QUERY_VECTOR_FILE` | Required in offline mode; JSON numeric array |
| `QUERY_TEXT` | Sample hotel query; used only in model mode |
| `EMBEDDING_DIMENSIONS` | `1536`; integer 1–2000 |
| `LOAD_SIZE_BATCH` | `100`; integer 1–1000 |
| `TOP_K` | `5`; integer 1–1000 |
| `VECTOR_SEARCH_TIMEOUT` | `5m`; duration 1s–30m |
| `AZURE_OPENAI_EMBEDDING_ENDPOINT` / `AZURE_OPENAI_EMBEDDING_MODEL` | Required only for model mode |
| `AZURE_OPENAI_EMBEDDING_KEY` | Optional secret; otherwise Entra |

Malformed integer, duration, mode, namespace, and boolean values fail closed, rather than silently becoming zero or selecting a fallback.

## Tests and evidence boundaries

```sh
go build ./...
go test -race -count=1 ./...
go vet ./...
```

Permanent tests exercise the real configuration, token callback, seed, index, search, cursor/error, and embedding-response functions. MongoDB wire-command mocks and an in-process HTTP transport avoid Docker, network/database mutations, model billing, or cloud credentials. CI runs these checks only.

The separately tagged **live integration** gate requires all the quick-start variables, a query vector exactly matching a seed document, and explicit additional authorization:

```sh
export VECTOR_SEARCH_LIVE_TEST=1
go test -tags=integration ./internal/documentdb -run '^TestLiveAlgorithms$' -count=1 -v -timeout 30m
```

Use absolute data/query paths because `go test` runs in the package directory. Leave `MONGO_COLLECTION` unset. The gate runs all three algorithms, starts with a populated collection to verify index creation is independent of data emptiness, checks the stored-vector self-match, snapshots/preserves existing documents, and repeats each workflow to assert zero duplicate inserts and index compatibility. Run it separately in **OIDC** and **native connection-string** modes to prove both authentication paths. The database is not dropped afterward.

Selecting `-tags=integration` without `VECTOR_SEARCH_LIVE_TEST=1` **fails**, rather than skips. Ordinary unit tests exclude the tagged live file; their success is not an Azure claim. The gate does not benchmark quality, latency, throughput, or real model relevance, and does not provision resources or conceal failures caused by unsupported tiers or permissions.

## Provisioning, maintenance, and license

Provisioning remains [upstream](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/infra), not copied here. Review firewall/admin/password defaults and costs before using any template. This repository contains no automatic deployments or cloud credentials.

This is a standalone downstream snapshot, not an automatically synchronized fork. Review and port upstream changes manually, retaining attribution and updating [UPSTREAM.md](UPSTREAM.md). Dependencies are pinned; the MongoDB Go v1 driver is retained from the source baseline, not represented as the newest driver generation.
