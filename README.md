# vector-search-go

**Go**. Load hotel data, generate query embeddings, and search with IVF, HNSW, and DiskANN indexes.

Standalone extraction of [the upstream sample](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/ai/vector-search-go). See [UPSTREAM.md](UPSTREAM.md) for attribution, exact revision, changes, and maintenance policy.

## Before you run

Use **Go 1.24.12 (the declared toolchain)**, Git, and Azure CLI or another supported Azure Identity sign-in mechanism.
Use an existing, explicitly approved disposable Azure DocumentDB environment and an Azure OpenAI embedding deployment compatible with `text-embedding-3-small` and 1536-dimensional vectors.
Your identity needs appropriate model access and a registered DocumentDB database identity with permissions for the sample operations. Network access must be configured deliberately; signing in alone does not grant database access.

**These are cloud-mutating samples, not production services or read-only demos.** They can create/drop indexes, replace/drop collections, and invoke billable models. Some programs hardcode `Hotels` or collection names; an environment-variable change alone is not a universal isolation guarantee.

Upstream documentation differs on minimum DiskANN tiers and model/API versions. Confirm current service requirements for your selected region and tier; the shared provisioning template currently chooses M40. Do not infer live readiness from source code, saved output, or syntax/build CI.

## Setup

Commands below use PowerShell and run from this repository root. They are instructions for a separately approved run; no live run is claimed by this extraction.

### 1. Retrieve the pinned sample data

Follow [data/README.md](data/README.md). It provides explicit downloads, source links, and SHA-256 checks. No data download or model call happens automatically on clone or install.

### 2. Configure the sample

Copy the placeholder-only example, then edit `.env` with your approved resource configuration. Do not commit `.env`.

```powershell
if (Test-Path -LiteralPath .env) { throw '.env already exists; edit it instead of overwriting it.' }
Copy-Item -LiteralPath .env.example -Destination .env
```

After replacing every `<placeholder>`, import these plain `KEY=value` lines into this PowerShell process. Keep values unquoted and put comments on separate lines; this reads data without executing the file:

```powershell
Get-Content -LiteralPath .env | ForEach-Object {
    $line = $_.Trim()
    if ($line.Length -eq 0 -or $line.StartsWith('#')) { return }
    if ($line -notmatch '^([A-Z][A-Z0-9_]*)=(.*)$') { throw 'Invalid .env line; expected KEY=value or a comment.' }
    [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2], 'Process')
}
```

The explicit import also covers samples that do not load dotenv files themselves. Node scripts and Go/Python dotenv helpers may additionally read the same repository-local `.env`.

For an interactive passwordless run, sign in to the intended Azure identity with `az login`; confirm the resource permissions and database identity independently.

### 3. Install/build when you are ready

```powershell
go mod download
```

### 4. Run only against the approved disposable environment

```powershell
go run ./cmd/ivf
go run ./cmd/hnsw
go run ./cmd/diskann
```

Each sample is its own command under `cmd/`, sharing helpers from `internal/documentdb`, so `go build ./...` and `go vet ./...` work on the whole repository. Optional embedding generation: `go run ./cmd/create-embeddings`. To inspect existing indexes: `go run ./cmd/show-indexes`.

## Shared provisioning

Provisioning remains [upstream](https://github.com/Azure-Samples/documentdb-samples/tree/f5a2675bbf349737234d0391e3ebee24b260e06f/infra), not duplicated here. If you choose azd, use a separate checkout of the [upstream repository root](https://github.com/Azure-Samples/documentdb-samples/blob/f5a2675bbf349737234d0391e3ebee24b260e06f/README.md), not this sample directory.
Review and harden its broad firewall/admin defaults and fallback administrator password before any deployment. No infrastructure, cloud credentials, GitHub Actions workflows, or automatic deployments are included in this extraction.

## Maintenance and license

This is a standalone downstream snapshot, not a GitHub fork of the complete monorepo. Upstream fixes must be reviewed and ported manually; update the recorded revision in [UPSTREAM.md](UPSTREAM.md) when doing so.
Dependencies and sample algorithms are retained from the pinned source. Static extraction checks do not certify dependency compatibility, authentication, deployment, or model behavior.

Sample code is Copyright (c) Microsoft Corporation, under the preserved [MIT license](LICENSE.md). Dataset source/terms remain linked in the data guide.
