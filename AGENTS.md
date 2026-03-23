# AGENTS.md — Instructions for AI Agents

## What This Repo Does

arxiv-archive is a Go binary that provides a local mirror of arXiv CS/AI/ML papers. It stores paper metadata, full text, embeddings, and citation graphs in PostgreSQL with pgvector. It exposes both a CLI and HTTP API for search, similarity, and citation traversal.

## How to Use This Repo

### As a dependency (HTTP API)
```bash
# Start the server
POSTGRES_URL=postgres://... archive serve --addr :9090

# Query papers
GET http://localhost:9090/papers/search?q=attention+mechanism
GET http://localhost:9090/papers/similar/2301.12345
GET http://localhost:9090/papers/recent?cat=cs.AI&days=7
GET http://localhost:9090/papers/2301.12345/refs
```

### As a CLI tool
```bash
archive search "transformer architecture"
archive similar 2301.12345
archive read 2301.12345
archive refs 2301.12345
archive stats
```

### Integration with other services
- The HTTP API returns JSON responses suitable for programmatic consumption
- The `/papers/similar?q=...` endpoint accepts free-text queries and returns semantically similar papers
- The `/sync` POST endpoint triggers background sync and returns immediately
- Citation graph endpoints (`/refs`, `/cited-by`) enable graph traversal

## API Response Format

All endpoints return JSON with consistent structure:
- Success: `{"count": N, "papers": [...]}` or `{"status": "ok"}`
- Error: `{"error": "message"}`

Paper objects contain: `arxiv_id`, `title`, `abstract`, `authors`, `categories`, `published`, `has_full_text`

## Data Coverage

Categories: cs.AI, cs.CL, cs.LG, cs.SE, cs.CV, stat.ML
Papers: ~800K with daily incremental sync
Embeddings: 1536-dim vectors via text-embedding-3-small

## Configuration

Set `POSTGRES_URL` (required) and optionally `LLM_ROUTER_URL` for embedding-powered similarity search.
