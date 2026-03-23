# CLAUDE.md — Instructions for Claude Code

## Project Overview

arxiv-archive is a Go binary that mirrors arXiv CS/AI/ML papers locally with PostgreSQL + pgvector for semantic search. It syncs paper metadata via OAI-PMH, fetches full text from Semantic Scholar, generates embeddings via llm-router, and serves an HTTP REST API.

## Build & Test

```bash
make build          # Build the binary
make test           # Run all tests with -race
make test-short     # Run tests without integration tests
make lint           # go vet + gofmt check
go build ./...      # Verify compilation
```

## Project Structure

- `main.go` — CLI entry point using cobra. 7 commands: sync, search, similar, read, refs, serve, stats.
- `internal/config/` — Environment variable loading. POSTGRES_URL is required. Other vars have defaults.
- `internal/db/` — PostgreSQL connection pool (pgx), schema migrations, paper CRUD, search, vector similarity, citation graph queries.
- `internal/harvest/` — Data fetching: OAI-PMH client (oaipmh.go), Semantic Scholar client (s2orc.go), embedding client (embed.go), sync orchestrator (sync.go).
- `internal/api/` — HTTP REST server with routing, middleware, and JSON responses.

## Key Design Decisions

- **Single binary**: All functionality in one binary with cobra subcommands.
- **pgvector**: Embeddings stored as `vector(1536)` columns, searched with cosine distance (`<=>`).
- **IVFFlat index**: Only created when 1000+ embedded papers exist (index needs data to train).
- **Batch operations**: Papers and embeddings are upserted in batches within transactions.
- **Polite harvesting**: 1-second delay between OAI-PMH requests, exponential backoff on errors.
- **Resumption tokens**: OAI-PMH tokens saved to sync_state table for crash recovery.
- **Rate limiting**: S2 client self-rate-limits (10 req/sec without key, 100 with key).

## Environment Variables

| Variable | Required | Default |
|----------|----------|---------|
| POSTGRES_URL | Yes | — |
| ARXIV_DATA_DIR | No | /srv/arxiv |
| LLM_ROUTER_URL | No | — |
| S2_API_KEY | No | — |
| ARXIV_CATEGORIES | No | cs.AI,cs.CL,cs.LG,cs.SE,cs.CV,stat.ML |

## Common Tasks

### Adding a new API endpoint
1. Add handler method to `internal/api/server.go`
2. Register route in `registerRoutes()`
3. Follow existing pattern: parse params, call db, return JSON

### Adding a new CLI command
1. Add command function in `main.go` following existing pattern
2. Register with `rootCmd.AddCommand()` in `main()`

### Modifying the schema
1. Add migration SQL to `Migrate()` in `internal/db/db.go`
2. Use `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` for idempotency
3. Update relevant CRUD methods in papers.go or refs.go

### Adding a new sync step
1. Add step logic as a method on `Syncer` in `internal/harvest/sync.go`
2. Add the step constant and wire it into `Run()`
3. Update the CLI's `--step` flag help text

## Dependencies

- `github.com/jackc/pgx/v5` — PostgreSQL driver with native pgvector support
- `github.com/pgvector/pgvector-go` — Go types for pgvector
- `github.com/spf13/cobra` — CLI framework

## Testing

Tests use mock HTTP servers (httptest) for external API testing. No database required for unit tests. Integration tests hit the real arXiv/S2 APIs and are skipped in short mode.
