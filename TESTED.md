# Validation record — 2026-10-12

## Validated release scope

The **precomputed-vector quick start** was tested against a real managed Azure DocumentDB M30 cluster in West US 2: one shard, MongoDB 8.0 compatibility, 128 GiB storage, high availability disabled. Certificate and hostname verification remained enabled. Network access was limited to the test client's IP address.

This is not a local-database or mocked-database result. It does not establish production capacity, availability, semantic quality, or a performance benchmark.

## Managed Azure checks

| Check | Native SCRAM | Microsoft Entra OIDC |
| --- | --- | --- |
| Authenticated connection with verified TLS | Passed | Passed |
| IVF create, inspect, query | Passed | Passed |
| HNSW create, inspect, query | Passed | Passed |
| DiskANN create, inspect, query | Passed | Passed |
| Five results, known stored-vector self-match | Passed | Passed |
| Index creation with an already populated collection | Passed | Passed |
| Repeated seed inserts zero duplicates | Passed | Passed |
| User-edited record and unrelated sentinel survive reruns | Passed | Passed |

Fixture: 50 pinned hotel records, 1,536-dimensional vectors. The query was the first record's stored vector. Six collections (three algorithms per authentication mode) were tested. After adding an unrelated record, all six retained 51 records and the edited original record after reruns. This is a nearest-neighbor contract check, not natural-language relevance validation.

All three actual command-line programs were exercised with both auth modes. Scoped `show-indexes` returned the expected algorithm parameters. Wrong native credentials, disabled writes, an unsafe `Hotels` database target, and requested TLS bypass each failed with a nonzero process exit without printing credentials.

The tested implementation before this documentation-only record was commit `64120c4b00260546abd5e6cf2de06b33f50c0ad3`.

## Local checks

- Build, race-enabled tests and `go vet` passed on the local tested Go toolchains (1.24.12 and 1.26.1).
- Final suite: 30 top-level tests / 125 passing test events including subtests. No test-case failures or skips. Four thin command packages have no separate test files; their actual CLIs were exercised during Azure validation.
- Tests cover disposable scope, seed preservation, index conflicts, cancellation/error propagation, malformed responses, OIDC audience/expiry, credential-safe Azure OpenAI endpoint validation, and rejected redirects. Endpoint/redirect tests use the production client wiring with an in-process transport, not an arbitrary external endpoint.
- Full repository history was scanned with Gitleaks 8.30.1; no credential matches found. This is a scanner result, not a guarantee that every possible secret or sensitive item can be detected.

## Optional model path: not live-validated

The test subscription rejected creation of an Azure OpenAI S0 resource with `SpecialFeatureOrQuotaIdRequired`. **No live model or embedding-generation request was made.** The optional Azure OpenAI mode has local SDK transport/response tests, not end-to-end Azure model validation. It requires an eligible resource, a compatible deployed embedding model and appropriate authorization. There is no silent mock fallback.

The primary precomputed-vector quick start needs no Azure OpenAI account. “Offline” refers only to embedding generation; it still uses a real Azure DocumentDB resource and incurs database charges.

## Safe reproduction and cleanup

Use a fresh disposable database and explicit write opt-in, as described in the README. The sample never automatically drops a database, collection or index. The operator is responsible for removing owned test resources afterward. The validation deployment was separate from all existing workloads; no production or customer data was used.

No subscription identifiers, resource credentials, tokens or private infrastructure details are included in this record.
