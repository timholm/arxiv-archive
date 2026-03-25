// Package api implements the HTTP REST server for the arxiv-archive.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/timholm/arxiv-archive/internal/config"
	"github.com/timholm/arxiv-archive/internal/db"
	"github.com/timholm/arxiv-archive/internal/harvest"
)

// Server is the HTTP API server.
type Server struct {
	db     *db.DB
	cfg    *config.Config
	logger *log.Logger
	embed  *harvest.EmbedClient
	mux    *http.ServeMux
}

// NewServer creates a new HTTP API server.
func NewServer(database *db.DB, cfg *config.Config) *Server {
	s := &Server{
		db:     database,
		cfg:    cfg,
		logger: log.New(os.Stderr, "[api] ", log.LstdFlags),
		mux:    http.NewServeMux(),
	}

	if cfg.LLMRouterURL != "" {
		s.embed = harvest.NewEmbedClient(cfg.LLMRouterURL)
	}

	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /papers/search", s.handleSearch)
	s.mux.HandleFunc("GET /papers/similar", s.handleSimilarByQuery)
	s.mux.HandleFunc("GET /papers/recent", s.handleRecent)
	s.mux.HandleFunc("GET /refs/{id}", s.handleRefs)
	s.mux.HandleFunc("GET /cited-by/{id}", s.handleCitedBy)
	s.mux.HandleFunc("GET /similar/{id}", s.handleSimilarByID)
	s.mux.HandleFunc("GET /papers/{id}", s.handleGetPaper)
	s.mux.HandleFunc("GET /stats", s.handleStats)
	s.mux.HandleFunc("POST /sync", s.handleSync)
	s.mux.HandleFunc("GET /health", s.handleHealth)
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.middleware(s.mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Printf("listening on %s", addr)
	return srv.ListenAndServe()
}

// middleware wraps the handler with logging and CORS headers.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// CORS
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Wrap response writer to capture status code
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		s.logger.Printf("%s %s %d %s",
			r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// handleGetPaper returns a single paper by ID.
// GET /papers/:id
func (s *Server) handleGetPaper(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "missing paper ID")
		return
	}

	paper, err := s.db.GetPaper(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Also fetch reference counts
	outgoing, incoming, _ := s.db.RefCount(r.Context(), id)

	type paperResponse struct {
		*db.Paper
		RefCount    int64 `json:"ref_count"`
		CitedByCount int64 `json:"cited_by_count"`
		FullText    string `json:"full_text,omitempty"`
	}

	resp := &paperResponse{
		Paper:        paper,
		RefCount:     outgoing,
		CitedByCount: incoming,
	}

	// Read full text if available
	if paper.HasFullText && paper.FullTextPath != "" {
		if data, err := os.ReadFile(paper.FullTextPath); err == nil {
			resp.FullText = string(data)
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleSearch performs full-text search.
// GET /papers/search?q=...&limit=N
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		s.writeError(w, http.StatusBadRequest, "missing query parameter 'q'")
		return
	}

	limit := parseIntParam(r, "limit", 20)

	papers, err := s.db.SearchPapers(r.Context(), query, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"query":   query,
		"count":   len(papers),
		"papers":  papers,
	})
}

// handleSimilarByID finds papers similar to a given paper.
// GET /papers/similar/:id?limit=N
func (s *Server) handleSimilarByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "missing paper ID")
		return
	}

	limit := parseIntParam(r, "limit", 10)

	papers, err := s.db.SimilarByID(r.Context(), id, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"source": id,
		"count":  len(papers),
		"papers": papers,
	})
}

// handleSimilarByQuery embeds a free-text query and finds similar papers.
// GET /papers/similar?q=...&limit=N
func (s *Server) handleSimilarByQuery(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		s.writeError(w, http.StatusBadRequest, "missing query parameter 'q'")
		return
	}

	if s.embed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "embedding service not configured (set LLM_ROUTER_URL)")
		return
	}

	limit := parseIntParam(r, "limit", 10)

	// Embed the query
	embedding, err := s.embed.Embed(r.Context(), query)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("embedding failed: %v", err))
		return
	}

	papers, err := s.db.SimilarByVector(r.Context(), embedding, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"query":  query,
		"count":  len(papers),
		"papers": papers,
	})
}

// handleRefs returns papers cited by the given paper.
// GET /papers/:id/refs
func (s *Server) handleRefs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "missing paper ID")
		return
	}

	papers, err := s.db.GetRefs(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"source": id,
		"count":  len(papers),
		"refs":   papers,
	})
}

// handleCitedBy returns papers that cite the given paper.
// GET /papers/:id/cited-by
func (s *Server) handleCitedBy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, "missing paper ID")
		return
	}

	papers, err := s.db.GetCitedBy(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"source":   id,
		"count":    len(papers),
		"cited_by": papers,
	})
}

// handleRecent returns recently published papers.
// GET /papers/recent?cat=...&days=N&limit=N
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("cat")
	days := parseIntParam(r, "days", 7)
	limit := parseIntParam(r, "limit", 50)

	papers, err := s.db.RecentPapers(r.Context(), category, days, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": category,
		"days":     days,
		"count":    len(papers),
		"papers":   papers,
	})
}

// handleStats returns archive statistics.
// GET /stats
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetStats(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, stats)
}

// handleSync triggers a manual sync.
// POST /sync?step=metadata|fulltext|embed|refs
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	stepStr := r.URL.Query().Get("step")
	step := harvest.SyncStep(stepStr)

	// Validate step
	switch step {
	case harvest.StepAll, harvest.StepMetadata, harvest.StepFulltext, harvest.StepEmbed, harvest.StepRefs:
		// valid
	default:
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid step: %s (use metadata, fulltext, embed, refs, or empty for all)", stepStr))
		return
	}

	syncer := harvest.NewSyncer(s.db, s.cfg)

	// Run sync in background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()

		if err := syncer.Run(ctx, step); err != nil {
			s.logger.Printf("sync error: %v", err)
		}
	}()

	s.writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "started",
		"step":    string(step),
		"message": "sync started in background",
	})
}

// handleHealth returns a simple health check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Quick DB ping
	if err := s.db.Pool.Ping(r.Context()); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		s.logger.Printf("write JSON error: %v", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{
		"error": message,
	})
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultVal
	}
	return v
}
