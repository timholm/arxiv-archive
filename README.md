# arxiv-archive

Full local mirror of arXiv CS/AI/ML papers with PostgreSQL + pgvector. Semantic search, citation graph, HTTP API.

## What it does

- **Harvests** paper metadata from arXiv via OAI-PMH (incremental sync with resumption tokens)
- **Fetches** full text and references from Semantic Scholar S2ORC API
- **Embeds** abstracts via llm-router (OpenAI-compatible, 1536-dim vectors stored in pgvector)
- **Indexes** citation graphs (who cites whom)
- **Serves** an HTTP REST API for search, similarity, and citation traversal

Covers `cs.AI, cs.CL, cs.LG, cs.SE, cs.CV, stat.ML` (~800K papers).

## Install

```bash
go install github.com/timholm/arxiv-archive@latest
```

Or build from source:

```bash
git clone https://github.com/timholm/arxiv-archive.git
cd arxiv-archive
make build
```

## Prerequisites

- **PostgreSQL 15+** with [pgvector](https://github.com/pgvector/pgvector) extension
- **llm-router** (optional, for embeddings) — any OpenAI-compatible `/v1/embeddings` endpoint
- **Semantic Scholar API key** (optional, for higher rate limits)

## Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `POSTGRES_URL` | Yes | — | PostgreSQL connection string |
| `ARXIV_DATA_DIR` | No | `/srv/arxiv` | Directory for full-text files |
| `LLM_ROUTER_URL` | No | — | Base URL for embedding API |
| `S2_API_KEY` | No | — | Semantic Scholar API key |
| `ARXIV_CATEGORIES` | No | `cs.AI,cs.CL,cs.LG,cs.SE,cs.CV,stat.ML` | Categories to sync |

## CLI Commands

```bash
# Full sync pipeline (metadata → fulltext → embed → refs)
archive sync

# Run individual sync steps
archive sync --step metadata     # OAI-PMH harvest only
archive sync --step fulltext     # Semantic Scholar fetch only
archive sync --step embed        # Embedding generation only
archive sync --step refs         # Reference extraction only

# Search papers
archive search "attention is all you need"
archive search "transformer architecture" --limit 50

# Find similar papers
archive similar 2301.12345           # by arXiv ID (vector similarity)
archive similar "chain of thought"   # by free text (embeds query first)

# Read a paper
archive read 2301.12345
archive read 2301.12345 --json

# Citation graph
archive refs 2301.12345             # papers this one cites
archive refs 2301.12345 --cited-by  # papers that cite this one

# Start HTTP API server
archive serve --addr :9090

# Archive statistics
archive stats
archive stats --json
```

## HTTP API

The `archive serve` command starts a REST API server.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/papers/:id` | Full paper metadata + text + ref counts |
| `GET` | `/papers/search?q=...` | Full-text search (PostgreSQL ts_vector) |
| `GET` | `/papers/similar/:id` | Vector similarity from a paper |
| `GET` | `/papers/similar?q=...` | Vector similarity from free text |
| `GET` | `/papers/:id/refs` | Papers this one cites |
| `GET` | `/papers/:id/cited-by` | Papers that cite this one |
| `GET` | `/papers/recent?cat=...&days=N` | Recent papers by category |
| `GET` | `/stats` | Archive statistics |
| `POST` | `/sync?step=...` | Trigger sync (background) |
| `GET` | `/health` | Health check |

### Examples

```bash
# Search
curl "http://localhost:9090/papers/search?q=transformer+attention&limit=10"

# Similar papers
curl "http://localhost:9090/papers/similar/2301.12345"

# Recent papers in cs.AI from last 7 days
curl "http://localhost:9090/papers/recent?cat=cs.AI&days=7"

# Citation graph
curl "http://localhost:9090/papers/2301.12345/refs"
curl "http://localhost:9090/papers/2301.12345/cited-by"
```

## Database Schema

```sql
-- Paper metadata + embeddings
CREATE TABLE papers (
    arxiv_id       TEXT PRIMARY KEY,
    title          TEXT NOT NULL,
    abstract       TEXT,
    authors        TEXT,
    categories     TEXT,
    published      TIMESTAMP,
    updated        TIMESTAMP,
    doi            TEXT,
    journal_ref    TEXT,
    has_full_text  BOOLEAN DEFAULT FALSE,
    full_text_path TEXT,
    embedding      vector(1536),
    fetched_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Citation graph
CREATE TABLE refs (
    from_id TEXT NOT NULL,
    to_id   TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id)
);

-- Incremental sync state
CREATE TABLE sync_state (
    key   TEXT PRIMARY KEY,
    value TEXT
);
```

## Sync Pipeline

The sync runs in 4 steps, designed for daily incremental updates:

1. **OAI-PMH Harvest** — Fetches new paper metadata from arXiv. First sync pulls ~800K papers (2-3 days). Daily syncs take ~5 minutes for ~500-2000 new papers. Saves resumption tokens for crash recovery.

2. **S2ORC Full Text** — Queries Semantic Scholar for papers without full text. Saves text to flat files organized by category. Rate: 10 req/sec (100 with API key).

3. **Embed Abstracts** — Generates 1536-dim embeddings for papers without them. Batches 50 abstracts per API call via llm-router. Stored in pgvector column for cosine similarity search.

4. **Extract References** — Builds the citation graph from Semantic Scholar data. Inserts into `refs` table for bidirectional traversal (cites / cited-by).

## Deployment

Designed for Kubernetes:

```yaml
# Always-on API server
archive serve → K8s Deployment (port 9090)

# Daily sync
archive sync → K8s CronJob (4 AM daily)

# Database
PostgreSQL + pgvector → K8s StatefulSet

# Storage
Full text files → PVC (100Gi, nfs-zfs)
```

## Architecture

```
arxiv-archive
├── main.go                    # CLI entry point (cobra)
├── internal/
│   ├── config/config.go       # Env var loading
│   ├── db/
│   │   ├── db.go              # PostgreSQL pool, migrations, stats
│   │   ├── papers.go          # Paper CRUD + search + similarity
│   │   └── refs.go            # Citation graph CRUD
│   ├── harvest/
│   │   ├── oaipmh.go          # OAI-PMH client for arXiv
│   │   ├── s2orc.go           # Semantic Scholar API client
│   │   ├── embed.go           # Embedding via llm-router
│   │   └── sync.go            # Pipeline orchestrator
│   └── api/server.go          # HTTP REST server
├── go.mod
├── Makefile
└── README.md
```

## License

MIT. Copyright 2026 Tim Holmquist.
