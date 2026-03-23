package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// Paper represents a single arXiv paper.
type Paper struct {
	ArxivID      string     `json:"arxiv_id"`
	Title        string     `json:"title"`
	Abstract     string     `json:"abstract,omitempty"`
	Authors      string     `json:"authors,omitempty"`
	Categories   string     `json:"categories,omitempty"`
	Published    *time.Time `json:"published,omitempty"`
	Updated      *time.Time `json:"updated,omitempty"`
	DOI          string     `json:"doi,omitempty"`
	JournalRef   string     `json:"journal_ref,omitempty"`
	HasFullText  bool       `json:"has_full_text"`
	FullTextPath string     `json:"full_text_path,omitempty"`
	Embedding    []float32  `json:"embedding,omitempty"`
	FetchedAt    *time.Time `json:"fetched_at,omitempty"`
}

// UpsertPaper inserts or updates a paper in the database.
func (db *DB) UpsertPaper(ctx context.Context, p *Paper) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO papers (arxiv_id, title, abstract, authors, categories, published, updated, doi, journal_ref, has_full_text, full_text_path, fetched_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, CURRENT_TIMESTAMP)
		 ON CONFLICT (arxiv_id) DO UPDATE SET
			title = EXCLUDED.title,
			abstract = EXCLUDED.abstract,
			authors = COALESCE(EXCLUDED.authors, papers.authors),
			categories = COALESCE(EXCLUDED.categories, papers.categories),
			published = COALESCE(EXCLUDED.published, papers.published),
			updated = COALESCE(EXCLUDED.updated, papers.updated),
			doi = COALESCE(EXCLUDED.doi, papers.doi),
			journal_ref = COALESCE(EXCLUDED.journal_ref, papers.journal_ref),
			has_full_text = EXCLUDED.has_full_text OR papers.has_full_text,
			full_text_path = COALESCE(EXCLUDED.full_text_path, papers.full_text_path),
			fetched_at = CURRENT_TIMESTAMP`,
		p.ArxivID, p.Title, p.Abstract, p.Authors, p.Categories,
		p.Published, p.Updated, p.DOI, p.JournalRef,
		p.HasFullText, p.FullTextPath,
	)
	if err != nil {
		return fmt.Errorf("upsert paper %s: %w", p.ArxivID, err)
	}
	return nil
}

// UpsertPapersBatch inserts or updates multiple papers in a single transaction.
func (db *DB) UpsertPapersBatch(ctx context.Context, papers []*Paper) (int64, error) {
	if len(papers) == 0 {
		return 0, nil
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var count int64
	for _, p := range papers {
		tag, err := tx.Exec(ctx,
			`INSERT INTO papers (arxiv_id, title, abstract, authors, categories, published, updated, doi, journal_ref, has_full_text, full_text_path, fetched_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, CURRENT_TIMESTAMP)
			 ON CONFLICT (arxiv_id) DO UPDATE SET
				title = EXCLUDED.title,
				abstract = EXCLUDED.abstract,
				authors = COALESCE(EXCLUDED.authors, papers.authors),
				categories = COALESCE(EXCLUDED.categories, papers.categories),
				published = COALESCE(EXCLUDED.published, papers.published),
				updated = COALESCE(EXCLUDED.updated, papers.updated),
				doi = COALESCE(EXCLUDED.doi, papers.doi),
				journal_ref = COALESCE(EXCLUDED.journal_ref, papers.journal_ref),
				has_full_text = EXCLUDED.has_full_text OR papers.has_full_text,
				full_text_path = COALESCE(EXCLUDED.full_text_path, papers.full_text_path),
				fetched_at = CURRENT_TIMESTAMP`,
			p.ArxivID, p.Title, p.Abstract, p.Authors, p.Categories,
			p.Published, p.Updated, p.DOI, p.JournalRef,
			p.HasFullText, p.FullTextPath,
		)
		if err != nil {
			return count, fmt.Errorf("upsert paper %s: %w", p.ArxivID, err)
		}
		count += tag.RowsAffected()
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}

// GetPaper retrieves a single paper by arXiv ID.
func (db *DB) GetPaper(ctx context.Context, arxivID string) (*Paper, error) {
	p := &Paper{}
	var emb *pgvector.Vector

	err := db.Pool.QueryRow(ctx,
		`SELECT arxiv_id, title, abstract, authors, categories, published, updated,
				doi, journal_ref, has_full_text, full_text_path, embedding, fetched_at
		 FROM papers WHERE arxiv_id = $1`, arxivID,
	).Scan(
		&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
		&p.Published, &p.Updated, &p.DOI, &p.JournalRef,
		&p.HasFullText, &p.FullTextPath, &emb, &p.FetchedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("paper %s not found", arxivID)
		}
		return nil, fmt.Errorf("get paper %s: %w", arxivID, err)
	}

	if emb != nil {
		p.Embedding = emb.Slice()
	}

	return p, nil
}

// SearchPapers performs full-text search using PostgreSQL ts_vector.
func (db *DB) SearchPapers(ctx context.Context, query string, limit int) ([]*Paper, error) {
	if limit <= 0 {
		limit = 20
	}

	// Use plainto_tsquery for natural language queries
	rows, err := db.Pool.Query(ctx,
		`SELECT arxiv_id, title, abstract, authors, categories, published, updated,
				doi, journal_ref, has_full_text, fetched_at,
				ts_rank(to_tsvector('english', COALESCE(title, '') || ' ' || COALESCE(abstract, '')),
						plainto_tsquery('english', $1)) AS rank
		 FROM papers
		 WHERE to_tsvector('english', COALESCE(title, '') || ' ' || COALESCE(abstract, ''))
			   @@ plainto_tsquery('english', $1)
		 ORDER BY rank DESC
		 LIMIT $2`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search papers: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		var rank float64
		if err := rows.Scan(
			&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&p.Published, &p.Updated, &p.DOI, &p.JournalRef,
			&p.HasFullText, &p.FetchedAt, &rank,
		); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// SimilarByID finds papers similar to the given paper using pgvector cosine distance.
func (db *DB) SimilarByID(ctx context.Context, arxivID string, limit int) ([]*Paper, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := db.Pool.Query(ctx,
		`SELECT p.arxiv_id, p.title, p.abstract, p.authors, p.categories,
				p.published, p.updated, p.has_full_text, p.fetched_at,
				1 - (p.embedding <=> ref.embedding) AS similarity
		 FROM papers p, papers ref
		 WHERE ref.arxiv_id = $1
			AND p.arxiv_id != $1
			AND p.embedding IS NOT NULL
			AND ref.embedding IS NOT NULL
		 ORDER BY p.embedding <=> ref.embedding
		 LIMIT $2`,
		arxivID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("similar by id: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		var similarity float64
		if err := rows.Scan(
			&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&p.Published, &p.Updated, &p.HasFullText, &p.FetchedAt, &similarity,
		); err != nil {
			return nil, fmt.Errorf("scan similar result: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// SimilarByVector finds papers similar to the given embedding vector.
func (db *DB) SimilarByVector(ctx context.Context, embedding []float32, limit int) ([]*Paper, error) {
	if limit <= 0 {
		limit = 10
	}

	vec := pgvector.NewVector(embedding)

	rows, err := db.Pool.Query(ctx,
		`SELECT arxiv_id, title, abstract, authors, categories,
				published, updated, has_full_text, fetched_at,
				1 - (embedding <=> $1::vector) AS similarity
		 FROM papers
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		vec, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("similar by vector: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		var similarity float64
		if err := rows.Scan(
			&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&p.Published, &p.Updated, &p.HasFullText, &p.FetchedAt, &similarity,
		); err != nil {
			return nil, fmt.Errorf("scan similar result: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// RecentPapers retrieves recently published papers, optionally filtered by category.
func (db *DB) RecentPapers(ctx context.Context, category string, days, limit int) ([]*Paper, error) {
	if days <= 0 {
		days = 7
	}
	if limit <= 0 {
		limit = 50
	}

	var rows pgx.Rows
	var err error

	if category != "" {
		rows, err = db.Pool.Query(ctx,
			`SELECT arxiv_id, title, abstract, authors, categories,
					published, updated, has_full_text, fetched_at
			 FROM papers
			 WHERE published >= NOW() - ($1 || ' days')::INTERVAL
				AND categories LIKE '%' || $2 || '%'
			 ORDER BY published DESC
			 LIMIT $3`,
			fmt.Sprintf("%d", days), category, limit,
		)
	} else {
		rows, err = db.Pool.Query(ctx,
			`SELECT arxiv_id, title, abstract, authors, categories,
					published, updated, has_full_text, fetched_at
			 FROM papers
			 WHERE published >= NOW() - ($1 || ' days')::INTERVAL
			 ORDER BY published DESC
			 LIMIT $2`,
			fmt.Sprintf("%d", days), limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("recent papers: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		if err := rows.Scan(
			&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&p.Published, &p.Updated, &p.HasFullText, &p.FetchedAt,
		); err != nil {
			return nil, fmt.Errorf("scan recent paper: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// UpdateEmbedding sets the embedding vector for a paper.
func (db *DB) UpdateEmbedding(ctx context.Context, arxivID string, embedding []float32) error {
	vec := pgvector.NewVector(embedding)
	_, err := db.Pool.Exec(ctx,
		`UPDATE papers SET embedding = $1 WHERE arxiv_id = $2`,
		vec, arxivID,
	)
	if err != nil {
		return fmt.Errorf("update embedding for %s: %w", arxivID, err)
	}
	return nil
}

// UpdateEmbeddingsBatch updates embeddings for multiple papers in a transaction.
func (db *DB) UpdateEmbeddingsBatch(ctx context.Context, ids []string, embeddings [][]float32) error {
	if len(ids) != len(embeddings) {
		return fmt.Errorf("ids and embeddings length mismatch: %d vs %d", len(ids), len(embeddings))
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for i, id := range ids {
		vec := pgvector.NewVector(embeddings[i])
		if _, err := tx.Exec(ctx,
			`UPDATE papers SET embedding = $1 WHERE arxiv_id = $2`,
			vec, id,
		); err != nil {
			return fmt.Errorf("update embedding for %s: %w", id, err)
		}
	}

	return tx.Commit(ctx)
}

// PapersWithoutEmbedding returns papers that have an abstract but no embedding.
func (db *DB) PapersWithoutEmbedding(ctx context.Context, limit int) ([]*Paper, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := db.Pool.Query(ctx,
		`SELECT arxiv_id, title, abstract, categories, published
		 FROM papers
		 WHERE embedding IS NULL
			AND abstract IS NOT NULL
			AND abstract != ''
		 ORDER BY published DESC NULLS LAST
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("papers without embedding: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		if err := rows.Scan(&p.ArxivID, &p.Title, &p.Abstract, &p.Categories, &p.Published); err != nil {
			return nil, fmt.Errorf("scan paper: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// PapersWithoutFullText returns papers that don't have full text downloaded yet.
func (db *DB) PapersWithoutFullText(ctx context.Context, limit int) ([]*Paper, error) {
	if limit <= 0 {
		limit = 500
	}

	rows, err := db.Pool.Query(ctx,
		`SELECT arxiv_id, title, categories, published
		 FROM papers
		 WHERE has_full_text = FALSE
		 ORDER BY published DESC NULLS LAST
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("papers without full text: %w", err)
	}
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		if err := rows.Scan(&p.ArxivID, &p.Title, &p.Categories, &p.Published); err != nil {
			return nil, fmt.Errorf("scan paper: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}

// UpdateFullText marks a paper as having full text at the given path.
func (db *DB) UpdateFullText(ctx context.Context, arxivID, path string) error {
	_, err := db.Pool.Exec(ctx,
		`UPDATE papers SET has_full_text = TRUE, full_text_path = $1 WHERE arxiv_id = $2`,
		path, arxivID,
	)
	if err != nil {
		return fmt.Errorf("update full text for %s: %w", arxivID, err)
	}
	return nil
}

// CountPapers returns various paper counts for reporting.
func (db *DB) CountPapers(ctx context.Context) (total, withText, withEmbed int64, err error) {
	err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM papers`).Scan(&total)
	if err != nil {
		return
	}
	err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM papers WHERE has_full_text = TRUE`).Scan(&withText)
	if err != nil {
		return
	}
	err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM papers WHERE embedding IS NOT NULL`).Scan(&withEmbed)
	return
}

// AllPaperIDs returns all arxiv IDs in the database. Used for dedup checks during sync.
func (db *DB) AllPaperIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := db.Pool.Query(ctx, `SELECT arxiv_id FROM papers`)
	if err != nil {
		return nil, fmt.Errorf("all paper ids: %w", err)
	}
	defer rows.Close()

	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan paper id: %w", err)
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// NormalizeArxivID normalizes arXiv IDs by removing version suffixes and the oai: prefix.
func NormalizeArxivID(raw string) string {
	// Remove oai:arXiv.org: prefix
	id := strings.TrimPrefix(raw, "oai:arXiv.org:")

	// Remove version suffix (e.g., v1, v2)
	if idx := strings.LastIndex(id, "v"); idx > 0 {
		suffix := id[idx+1:]
		allDigits := true
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && len(suffix) > 0 {
			id = id[:idx]
		}
	}

	return id
}
