// arxiv-archive: Full local mirror of arXiv CS/AI/ML papers with PostgreSQL + pgvector.
// Provides semantic search, citation graph traversal, and an HTTP REST API.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/timholm/arxiv-archive/internal/api"
	"github.com/timholm/arxiv-archive/internal/config"
	"github.com/timholm/arxiv-archive/internal/db"
	"github.com/timholm/arxiv-archive/internal/harvest"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "archive",
		Short:   "Full local mirror of arXiv CS/AI/ML papers with semantic search",
		Version: fmt.Sprintf("%s (built %s)", version, buildTime),
	}

	rootCmd.AddCommand(
		syncCmd(),
		searchCmd(),
		similarCmd(),
		readCmd(),
		refsCmd(),
		serveCmd(),
		statsCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func syncCmd() *cobra.Command {
	var step string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Run the sync pipeline (metadata, fulltext, embed, refs)",
		Long: `Sync fetches new papers from arXiv via OAI-PMH, downloads full text
from Semantic Scholar, generates embeddings via llm-router, and extracts
citation references.

Steps can be run individually with --step:
  metadata  - OAI-PMH harvest only
  fulltext  - Semantic Scholar full text fetch only
  embed     - Embedding generation only
  refs      - Reference extraction only`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			if err := database.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}

			syncer := harvest.NewSyncer(database, cfg)
			return syncer.Run(ctx, harvest.SyncStep(step))
		},
	}

	cmd.Flags().StringVar(&step, "step", "", "run only a specific step (metadata, fulltext, embed, refs)")
	return cmd
}

func searchCmd() *cobra.Command {
	var limit int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Full-text search for papers",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx := context.Background()
			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			papers, err := database.SearchPapers(ctx, query, limit)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			if len(papers) == 0 {
				fmt.Println("No papers found.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(papers)
			}

			printPaperList(papers)
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "maximum results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func similarCmd() *cobra.Command {
	var limit int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "similar [arxiv-id or query text]",
		Short: "Find similar papers by vector similarity",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := strings.Join(args, " ")

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx := context.Background()
			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			var papers []*db.Paper

			// If the input looks like an arXiv ID, use vector similarity by ID
			if looksLikeArxivID(input) {
				papers, err = database.SimilarByID(ctx, input, limit)
				if err != nil {
					return fmt.Errorf("similar: %w", err)
				}
			} else {
				// Otherwise, embed the query and search by vector
				if cfg.LLMRouterURL == "" {
					return fmt.Errorf("LLM_ROUTER_URL required for free-text similarity search")
				}

				embedClient := harvest.NewEmbedClient(cfg.LLMRouterURL)
				embedding, err := embedClient.Embed(ctx, input)
				if err != nil {
					return fmt.Errorf("embed query: %w", err)
				}

				papers, err = database.SimilarByVector(ctx, embedding, limit)
				if err != nil {
					return fmt.Errorf("similar: %w", err)
				}
			}

			if len(papers) == 0 {
				fmt.Println("No similar papers found.")
				return nil
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(papers)
			}

			printPaperList(papers)
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "maximum results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func readCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "read [arxiv-id]",
		Short: "Print a paper's full metadata and text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arxivID := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx := context.Background()
			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			paper, err := database.GetPaper(ctx, arxivID)
			if err != nil {
				return fmt.Errorf("get paper: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(paper)
			}

			fmt.Printf("Title:      %s\n", paper.Title)
			fmt.Printf("arXiv ID:   %s\n", paper.ArxivID)
			fmt.Printf("Authors:    %s\n", paper.Authors)
			fmt.Printf("Categories: %s\n", paper.Categories)
			if paper.Published != nil {
				fmt.Printf("Published:  %s\n", paper.Published.Format("2006-01-02"))
			}
			if paper.DOI != "" {
				fmt.Printf("DOI:        %s\n", paper.DOI)
			}
			fmt.Printf("Full Text:  %v\n", paper.HasFullText)
			fmt.Println()

			if paper.Abstract != "" {
				fmt.Println("--- Abstract ---")
				fmt.Println(paper.Abstract)
				fmt.Println()
			}

			if paper.HasFullText && paper.FullTextPath != "" {
				data, err := os.ReadFile(paper.FullTextPath)
				if err == nil {
					fmt.Println("--- Full Text ---")
					fmt.Println(string(data))
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func refsCmd() *cobra.Command {
	var jsonOutput bool
	var showCitedBy bool

	cmd := &cobra.Command{
		Use:   "refs [arxiv-id]",
		Short: "Show citation graph for a paper",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arxivID := args[0]

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx := context.Background()
			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			if showCitedBy {
				papers, err := database.GetCitedBy(ctx, arxivID)
				if err != nil {
					return fmt.Errorf("cited-by: %w", err)
				}

				if jsonOutput {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(papers)
				}

				fmt.Printf("Papers that cite %s (%d):\n\n", arxivID, len(papers))
				printPaperList(papers)
			} else {
				papers, err := database.GetRefs(ctx, arxivID)
				if err != nil {
					return fmt.Errorf("refs: %w", err)
				}

				if jsonOutput {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(papers)
				}

				fmt.Printf("Papers cited by %s (%d):\n\n", arxivID, len(papers))
				printPaperList(papers)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	cmd.Flags().BoolVar(&showCitedBy, "cited-by", false, "show papers that cite this one")
	return cmd
}

func serveCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			if err := database.Migrate(ctx); err != nil {
				return fmt.Errorf("migrate database: %w", err)
			}

			server := api.NewServer(database, cfg)

			// Graceful shutdown
			go func() {
				<-ctx.Done()
				fmt.Println("\nshutting down...")
				database.Close()
				os.Exit(0)
			}()

			return server.ListenAndServe(addr)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", ":9090", "listen address")
	return cmd
}

func statsCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show archive statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			ctx := context.Background()
			database, err := db.New(ctx, cfg.PostgresURL)
			if err != nil {
				return fmt.Errorf("connect to database: %w", err)
			}
			defer database.Close()

			stats, err := database.GetStats(ctx)
			if err != nil {
				return fmt.Errorf("get stats: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(stats)
			}

			fmt.Println("=== arxiv-archive stats ===")
			fmt.Printf("Total papers:       %d\n", stats.TotalPapers)
			fmt.Printf("With full text:     %d\n", stats.WithFullText)
			fmt.Printf("With embedding:     %d\n", stats.WithEmbedding)
			fmt.Printf("Total refs:         %d\n", stats.TotalRefs)
			fmt.Printf("Distinct categories: %d\n", stats.CategoriesCount)
			if !stats.OldestPaper.IsZero() && stats.OldestPaper.Year() > 1970 {
				fmt.Printf("Oldest paper:       %s\n", stats.OldestPaper.Format("2006-01-02"))
			}
			if !stats.NewestPaper.IsZero() && stats.NewestPaper.Year() > 1970 {
				fmt.Printf("Newest paper:       %s\n", stats.NewestPaper.Format("2006-01-02"))
			}
			fmt.Printf("Papers (7 days):    %d\n", stats.PapersLast7Days)
			fmt.Printf("Papers (30 days):   %d\n", stats.PapersLast30Days)
			if stats.LastSync != "" {
				fmt.Printf("Last sync:          %s\n", stats.LastSync)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// printPaperList prints papers in a tabular format.
func printPaperList(papers []*db.Paper) {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tTITLE\tDATE\tCATEGORIES\n")
	fmt.Fprintf(w, "---\t-----\t----\t----------\n")

	for _, p := range papers {
		title := p.Title
		if len(title) > 80 {
			title = title[:77] + "..."
		}
		date := ""
		if p.Published != nil {
			date = p.Published.Format("2006-01-02")
		}
		cats := p.Categories
		if len(cats) > 30 {
			cats = cats[:27] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ArxivID, title, date, cats)
	}
	w.Flush()
}

// looksLikeArxivID checks if a string looks like an arXiv paper ID.
// Matches patterns like: 2301.12345, hep-th/9901001, cs/0112017
func looksLikeArxivID(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, " ") {
		return false
	}
	// New-style: YYMM.NNNNN
	if len(s) >= 9 && s[4] == '.' {
		return true
	}
	// Old-style: category/YYMMNNNN
	if strings.Contains(s, "/") {
		return true
	}
	return false
}

