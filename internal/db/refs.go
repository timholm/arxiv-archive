package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Ref represents a citation link between two papers.
type Ref struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
}

// UpsertRef inserts a citation reference, ignoring duplicates.
func (db *DB) UpsertRef(ctx context.Context, fromID, toID string) error {
	_, err := db.Pool.Exec(ctx,
		`INSERT INTO refs (from_id, to_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		fromID, toID,
	)
	if err != nil {
		return fmt.Errorf("upsert ref %s -> %s: %w", fromID, toID, err)
	}
	return nil
}

// UpsertRefsBatch inserts multiple citation references in a transaction.
func (db *DB) UpsertRefsBatch(ctx context.Context, refs []Ref) (int64, error) {
	if len(refs) == 0 {
		return 0, nil
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var count int64
	for _, r := range refs {
		tag, err := tx.Exec(ctx,
			`INSERT INTO refs (from_id, to_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			r.FromID, r.ToID,
		)
		if err != nil {
			return count, fmt.Errorf("upsert ref %s -> %s: %w", r.FromID, r.ToID, err)
		}
		count += tag.RowsAffected()
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit refs: %w", err)
	}
	return count, nil
}

// GetRefs returns papers that the given paper cites (outgoing references).
func (db *DB) GetRefs(ctx context.Context, arxivID string) ([]*Paper, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT p.arxiv_id, p.title, p.abstract, p.authors, p.categories,
				p.published, p.updated, p.has_full_text, p.fetched_at
		 FROM refs r
		 JOIN papers p ON p.arxiv_id = r.to_id
		 WHERE r.from_id = $1
		 ORDER BY p.published DESC NULLS LAST`,
		arxivID,
	)
	if err != nil {
		return nil, fmt.Errorf("get refs for %s: %w", arxivID, err)
	}
	return scanPaperRows(rows)
}

// GetCitedBy returns papers that cite the given paper (incoming references).
func (db *DB) GetCitedBy(ctx context.Context, arxivID string) ([]*Paper, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT p.arxiv_id, p.title, p.abstract, p.authors, p.categories,
				p.published, p.updated, p.has_full_text, p.fetched_at
		 FROM refs r
		 JOIN papers p ON p.arxiv_id = r.from_id
		 WHERE r.to_id = $1
		 ORDER BY p.published DESC NULLS LAST`,
		arxivID,
	)
	if err != nil {
		return nil, fmt.Errorf("get cited-by for %s: %w", arxivID, err)
	}
	return scanPaperRows(rows)
}

// GetRefIDs returns the raw to_id values for papers cited by the given paper.
func (db *DB) GetRefIDs(ctx context.Context, arxivID string) ([]string, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT to_id FROM refs WHERE from_id = $1`, arxivID,
	)
	if err != nil {
		return nil, fmt.Errorf("get ref ids for %s: %w", arxivID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan ref id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetCitedByIDs returns the raw from_id values for papers that cite the given paper.
func (db *DB) GetCitedByIDs(ctx context.Context, arxivID string) ([]string, error) {
	rows, err := db.Pool.Query(ctx,
		`SELECT from_id FROM refs WHERE to_id = $1`, arxivID,
	)
	if err != nil {
		return nil, fmt.Errorf("get cited-by ids for %s: %w", arxivID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cited-by id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RefCount returns the number of outgoing and incoming references for a paper.
func (db *DB) RefCount(ctx context.Context, arxivID string) (outgoing, incoming int64, err error) {
	err = db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM refs WHERE from_id = $1`, arxivID,
	).Scan(&outgoing)
	if err != nil {
		return 0, 0, fmt.Errorf("count outgoing refs: %w", err)
	}

	err = db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM refs WHERE to_id = $1`, arxivID,
	).Scan(&incoming)
	if err != nil {
		return 0, 0, fmt.Errorf("count incoming refs: %w", err)
	}

	return outgoing, incoming, nil
}

// scanPaperRows is a helper that scans paper rows from a query result.
func scanPaperRows(rows pgx.Rows) ([]*Paper, error) {
	defer rows.Close()

	var papers []*Paper
	for rows.Next() {
		p := &Paper{}
		if err := rows.Scan(
			&p.ArxivID, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&p.Published, &p.Updated, &p.HasFullText, &p.FetchedAt,
		); err != nil {
			return nil, fmt.Errorf("scan paper row: %w", err)
		}
		papers = append(papers, p)
	}
	return papers, rows.Err()
}
