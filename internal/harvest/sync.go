package harvest

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timholm/arxiv-archive/internal/config"
	"github.com/timholm/arxiv-archive/internal/db"
)

// SyncStep identifies which part of the sync pipeline to run.
type SyncStep string

const (
	StepAll      SyncStep = ""
	StepMetadata SyncStep = "metadata"
	StepFulltext SyncStep = "fulltext"
	StepEmbed    SyncStep = "embed"
	StepRefs     SyncStep = "refs"
)

// Syncer orchestrates the full sync pipeline.
type Syncer struct {
	db     *db.DB
	cfg    *config.Config
	oai    *OAIPMHClient
	s2     *S2Client
	embed  *EmbedClient
	logger *log.Logger
}

// NewSyncer creates a new sync pipeline orchestrator.
func NewSyncer(database *db.DB, cfg *config.Config) *Syncer {
	return &Syncer{
		db:     database,
		cfg:    cfg,
		oai:    NewOAIPMHClient(),
		s2:     NewS2Client(cfg.S2APIKey),
		embed:  NewEmbedClient(cfg.LLMRouterURL),
		logger: log.New(os.Stderr, "[sync] ", log.LstdFlags),
	}
}

// Run executes the sync pipeline, optionally limited to a single step.
func (s *Syncer) Run(ctx context.Context, step SyncStep) error {
	start := time.Now()
	s.logger.Printf("starting sync (step=%s)", stepName(step))

	var err error

	switch step {
	case StepMetadata:
		err = s.syncMetadata(ctx)
	case StepFulltext:
		err = s.syncFullText(ctx)
	case StepEmbed:
		err = s.syncEmbeddings(ctx)
	case StepRefs:
		err = s.syncRefs(ctx)
	case StepAll:
		// Run all steps in sequence
		if err = s.syncMetadata(ctx); err != nil {
			return fmt.Errorf("metadata step: %w", err)
		}
		if err = s.syncFullText(ctx); err != nil {
			return fmt.Errorf("fulltext step: %w", err)
		}
		if err = s.syncEmbeddings(ctx); err != nil {
			return fmt.Errorf("embed step: %w", err)
		}
		if err = s.syncRefs(ctx); err != nil {
			return fmt.Errorf("refs step: %w", err)
		}
	default:
		return fmt.Errorf("unknown sync step: %s", step)
	}

	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	s.logger.Printf("sync completed in %s", elapsed.Round(time.Second))

	// Update sync state
	_ = s.db.SetSyncState(ctx, "last_sync", time.Now().UTC().Format(time.RFC3339))
	_ = s.db.SetSyncState(ctx, "last_sync_duration", elapsed.String())

	return nil
}

// syncMetadata harvests paper metadata from arXiv via OAI-PMH.
func (s *Syncer) syncMetadata(ctx context.Context) error {
	s.logger.Println("step 1: harvesting metadata via OAI-PMH")

	totalPapers := int64(0)

	for _, category := range s.cfg.Categories {
		s.logger.Printf("harvesting category: %s", category)

		// Convert "cs.AI" → "cs:cs:AI" for OAI-PMH set parameter.
		oaiSet := strings.ReplaceAll(category, ".", ":") // cs.AI → cs:AI
		if parts := strings.SplitN(category, ".", 2); len(parts) == 2 {
			oaiSet = parts[0] + ":" + parts[0] + ":" + parts[1] // cs:cs:AI
		}

		// Check for saved resumption token
		tokenKey := fmt.Sprintf("oai_token_%s", category)
		resumeToken, _ := s.db.GetSyncState(ctx, tokenKey)

		// Check for last harvest date
		dateKey := fmt.Sprintf("oai_last_%s", category)
		fromDate, _ := s.db.GetSyncState(ctx, dateKey)

		// If no resumption token and no from date, this is a first sync
		if resumeToken == "" && fromDate == "" {
			s.logger.Printf("  first sync for %s, harvesting all records", category)
		} else if resumeToken != "" {
			s.logger.Printf("  resuming from token for %s", category)
		} else {
			s.logger.Printf("  incremental sync from %s for %s", fromDate, category)
		}

		categoryPapers := int64(0)
		batchNum := 0

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			result, err := s.oai.Harvest(ctx, oaiSet, resumeToken, fromDate)
			if err != nil {
				// Save current token so we can resume later
				if resumeToken != "" {
					_ = s.db.SetSyncState(ctx, tokenKey, resumeToken)
				}
				return fmt.Errorf("harvest %s: %w", category, err)
			}

			if len(result.Papers) > 0 {
				count, err := s.db.UpsertPapersBatch(ctx, result.Papers)
				if err != nil {
					return fmt.Errorf("upsert papers: %w", err)
				}
				categoryPapers += count
				batchNum++

				if batchNum%10 == 0 || result.CompleteSize != "" {
					s.logger.Printf("  %s: batch %d, %d papers so far (total size: %s, cursor: %s)",
						category, batchNum, categoryPapers, result.CompleteSize, result.Cursor)
				}
			}

			// Check if we're done
			if result.ResumptionToken == "" {
				break
			}

			resumeToken = result.ResumptionToken
			// Save token periodically so we can resume on crash
			_ = s.db.SetSyncState(ctx, tokenKey, resumeToken)

			// Polite delay between requests
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(oaiPollDelay):
			}
		}

		// Clear saved token (harvest complete)
		_ = s.db.SetSyncState(ctx, tokenKey, "")
		// Update last harvest date
		_ = s.db.SetSyncState(ctx, dateKey, time.Now().UTC().Format("2006-01-02"))

		totalPapers += categoryPapers
		s.logger.Printf("  %s complete: %d papers", category, categoryPapers)
	}

	s.logger.Printf("metadata harvest complete: %d total papers upserted", totalPapers)
	return nil
}

// syncFullText fetches full text from Semantic Scholar for papers that don't have it.
func (s *Syncer) syncFullText(ctx context.Context) error {
	s.logger.Println("step 2: fetching full text via Semantic Scholar")

	papers, err := s.db.PapersWithoutFullText(ctx, 500)
	if err != nil {
		return fmt.Errorf("get papers without full text: %w", err)
	}

	if len(papers) == 0 {
		s.logger.Println("  no papers need full text")
		return nil
	}

	s.logger.Printf("  %d papers need full text", len(papers))

	// Process in batches of 50
	batchSize := 50
	fetched := 0
	failed := 0

	for i := 0; i < len(papers); i += batchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := i + batchSize
		if end > len(papers) {
			end = len(papers)
		}
		batch := papers[i:end]

		ids := make([]string, len(batch))
		for j, p := range batch {
			ids[j] = p.ArxivID
		}

		s2Papers, err := s.s2.GetPapersBatch(ctx, ids)
		if err != nil {
			s.logger.Printf("  batch fetch error: %v", err)
			failed += len(batch)
			continue
		}

		for _, s2p := range s2Papers {
			if s2p == nil {
				continue
			}

			arxivID := s2p.ExternalIDs.ArXiv
			if arxivID == "" {
				continue
			}

			// Extract abstract/TLDR if we have it
			if s2p.Abstract != "" {
				// We already have metadata, but update with S2's abstract if ours is empty
			}

			// Save full text if we have open access PDF info
			// For now, we store the S2 abstract/TLDR as the "full text" proxy
			// In production, you'd download and parse the actual PDF
			text := s2p.Abstract
			if s2p.TLDR != nil && s2p.TLDR.Text != "" {
				text = fmt.Sprintf("%s\n\nTL;DR: %s", text, s2p.TLDR.Text)
			}

			if text != "" {
				// Save to flat file
				cat := "unknown"
				// Try to get category from the paper
				for _, p := range batch {
					if p.ArxivID == arxivID && p.Categories != "" {
						parts := strings.Split(p.Categories, ",")
						if len(parts) > 0 {
							cat = strings.TrimSpace(parts[0])
						}
						break
					}
				}

				dir := filepath.Join(s.cfg.ArxivDataDir, strings.ReplaceAll(cat, ".", "/"))
				if err := os.MkdirAll(dir, 0755); err != nil {
					s.logger.Printf("  mkdir error for %s: %v", dir, err)
					failed++
					continue
				}

				filePath := filepath.Join(dir, arxivID+".txt")
				if err := os.WriteFile(filePath, []byte(text), 0644); err != nil {
					s.logger.Printf("  write error for %s: %v", filePath, err)
					failed++
					continue
				}

				if err := s.db.UpdateFullText(ctx, arxivID, filePath); err != nil {
					s.logger.Printf("  db update error for %s: %v", arxivID, err)
					failed++
					continue
				}

				fetched++
			}

			// Extract and store references
			refs := ExtractRefsFromS2Paper(s2p)
			if len(refs) > 0 {
				dbRefs := make([]db.Ref, len(refs))
				for i, refID := range refs {
					dbRefs[i] = db.Ref{FromID: arxivID, ToID: refID}
				}
				if _, err := s.db.UpsertRefsBatch(ctx, dbRefs); err != nil {
					s.logger.Printf("  refs error for %s: %v", arxivID, err)
				}
			}

			// Extract and store citations (reverse refs)
			cites := ExtractCitationsFromS2Paper(s2p)
			if len(cites) > 0 {
				dbRefs := make([]db.Ref, len(cites))
				for i, citeID := range cites {
					dbRefs[i] = db.Ref{FromID: citeID, ToID: arxivID}
				}
				if _, err := s.db.UpsertRefsBatch(ctx, dbRefs); err != nil {
					s.logger.Printf("  citations error for %s: %v", arxivID, err)
				}
			}
		}

		if (i/batchSize+1)%5 == 0 {
			s.logger.Printf("  progress: %d/%d (fetched: %d, failed: %d)",
				end, len(papers), fetched, failed)
		}
	}

	s.logger.Printf("full text complete: %d fetched, %d failed", fetched, failed)
	return nil
}

// syncEmbeddings generates embeddings for papers that don't have them.
func (s *Syncer) syncEmbeddings(ctx context.Context) error {
	if s.cfg.LLMRouterURL == "" {
		s.logger.Println("step 3: skipping embeddings (LLM_ROUTER_URL not set)")
		return nil
	}

	s.logger.Println("step 3: generating embeddings via llm-router")

	papers, err := s.db.PapersWithoutEmbedding(ctx, 500)
	if err != nil {
		return fmt.Errorf("get papers without embedding: %w", err)
	}

	if len(papers) == 0 {
		s.logger.Println("  no papers need embeddings")
		return nil
	}

	s.logger.Printf("  %d papers need embeddings", len(papers))

	embedded := 0
	failed := 0

	// Process in batches of 50
	for i := 0; i < len(papers); i += embedBatchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		end := i + embedBatchSize
		if end > len(papers) {
			end = len(papers)
		}
		batch := papers[i:end]

		texts := make([]string, len(batch))
		ids := make([]string, len(batch))
		for j, p := range batch {
			// Embed title + abstract for richer representation
			texts[j] = fmt.Sprintf("%s\n\n%s", p.Title, p.Abstract)
			ids[j] = p.ArxivID
		}

		embeddings, err := s.embed.EmbedBatch(ctx, texts)
		if err != nil {
			s.logger.Printf("  embedding batch error: %v", err)
			failed += len(batch)
			continue
		}

		if err := s.db.UpdateEmbeddingsBatch(ctx, ids, embeddings); err != nil {
			s.logger.Printf("  db update error: %v", err)
			failed += len(batch)
			continue
		}

		embedded += len(batch)

		if (i/embedBatchSize+1)%10 == 0 {
			s.logger.Printf("  progress: %d/%d embedded", embedded, len(papers))
		}
	}

	s.logger.Printf("embeddings complete: %d embedded, %d failed", embedded, failed)
	return nil
}

// syncRefs fetches references for papers that might be missing them.
func (s *Syncer) syncRefs(ctx context.Context) error {
	s.logger.Println("step 4: syncing references")

	// This step is mostly handled during fulltext fetch.
	// Here we do a targeted pass for any papers that might have been missed.
	var count int64
	err := s.db.Pool.QueryRow(ctx,
		`SELECT COUNT(DISTINCT p.arxiv_id)
		 FROM papers p
		 LEFT JOIN refs r ON r.from_id = p.arxiv_id
		 WHERE r.from_id IS NULL
		 AND p.has_full_text = TRUE
		 LIMIT 100`,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("count papers without refs: %w", err)
	}

	if count == 0 {
		s.logger.Println("  all full-text papers have refs")
		return nil
	}

	s.logger.Printf("  %d papers may need refs (handled during fulltext step)", count)
	return nil
}

func stepName(step SyncStep) string {
	if step == "" {
		return "all"
	}
	return string(step)
}
