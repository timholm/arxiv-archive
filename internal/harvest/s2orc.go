package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	s2BaseURL = "https://api.semanticscholar.org/graph/v1"
)

// S2Client is a client for the Semantic Scholar Academic Graph API.
type S2Client struct {
	client *http.Client
	apiKey string

	// Rate limiting
	rateDelay time.Duration
	lastReq   time.Time
}

// NewS2Client creates a new Semantic Scholar API client.
// With an API key, allows 100 req/sec. Without, 10 req/sec.
func NewS2Client(apiKey string) *S2Client {
	delay := 100 * time.Millisecond // 10 req/sec default
	if apiKey != "" {
		delay = 10 * time.Millisecond // 100 req/sec with key
	}

	return &S2Client{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiKey:    apiKey,
		rateDelay: delay,
	}
}

// S2Paper represents a paper from the Semantic Scholar API.
type S2Paper struct {
	PaperID         string        `json:"paperId"`
	ExternalIDs     S2ExternalIDs `json:"externalIds"`
	Title           string        `json:"title"`
	Abstract        string        `json:"abstract"`
	TLDR            *S2TLDR       `json:"tldr"`
	PublicationDate string        `json:"publicationDate"`
	OpenAccessPDF   *S2PDF        `json:"openAccessPdf"`
	References      []S2Ref       `json:"references"`
	Citations       []S2Ref       `json:"citations"`
}

// S2ExternalIDs holds external identifiers for a paper.
type S2ExternalIDs struct {
	ArXiv string `json:"ArXiv"`
	DOI   string `json:"DOI"`
}

// S2TLDR holds the auto-generated TL;DR summary.
type S2TLDR struct {
	Model string `json:"model"`
	Text  string `json:"text"`
}

// S2PDF holds the open access PDF URL.
type S2PDF struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

// S2Ref represents a reference or citation.
type S2Ref struct {
	PaperID     string        `json:"paperId"`
	ExternalIDs S2ExternalIDs `json:"externalIds"`
	Title       string        `json:"title"`
}

// GetPaper fetches a paper from Semantic Scholar by arXiv ID.
func (c *S2Client) GetPaper(ctx context.Context, arxivID string) (*S2Paper, error) {
	c.rateLimit()

	fields := "title,abstract,externalIds,references,citations,tldr,publicationDate,openAccessPdf"
	url := fmt.Sprintf("%s/paper/arXiv:%s?fields=%s", s2BaseURL, arxivID, fields)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch paper %s: %w", arxivID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("paper %s not found on Semantic Scholar", arxivID)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("S2 API error (status %d): %s", resp.StatusCode, string(body))
	}

	var paper S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&paper); err != nil {
		return nil, fmt.Errorf("decode S2 response: %w", err)
	}

	return &paper, nil
}

// S2BatchRequest is the request body for the batch paper endpoint.
type S2BatchRequest struct {
	IDs []string `json:"ids"`
}

// GetPapersBatch fetches multiple papers in a single request.
// IDs should be in the format "arXiv:XXXX.XXXXX".
func (c *S2Client) GetPapersBatch(ctx context.Context, arxivIDs []string) ([]*S2Paper, error) {
	if len(arxivIDs) == 0 {
		return nil, nil
	}

	c.rateLimit()

	// Build IDs with arXiv: prefix
	ids := make([]string, len(arxivIDs))
	for i, id := range arxivIDs {
		if !strings.HasPrefix(id, "arXiv:") {
			ids[i] = "arXiv:" + id
		} else {
			ids[i] = id
		}
	}

	fields := "title,abstract,externalIds,references,citations,tldr,publicationDate,openAccessPdf"
	url := fmt.Sprintf("%s/paper/batch?fields=%s", s2BaseURL, fields)

	body, err := json.Marshal(S2BatchRequest{IDs: ids})
	if err != nil {
		return nil, fmt.Errorf("marshal batch request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create batch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("batch fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("S2 batch API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var papers []*S2Paper
	if err := json.NewDecoder(resp.Body).Decode(&papers); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	return papers, nil
}

// GetOpenAccessPDFURL fetches just the open access PDF URL for a paper.
func (c *S2Client) GetOpenAccessPDFURL(ctx context.Context, arxivID string) (string, error) {
	c.rateLimit()

	url := fmt.Sprintf("%s/paper/arXiv:%s?fields=openAccessPdf", s2BaseURL, arxivID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(ctx, req)
	if err != nil {
		return "", fmt.Errorf("fetch PDF URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("S2 API error (status %d)", resp.StatusCode)
	}

	var result struct {
		OpenAccessPDF *S2PDF `json:"openAccessPdf"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if result.OpenAccessPDF == nil || result.OpenAccessPDF.URL == "" {
		return "", fmt.Errorf("no open access PDF for %s", arxivID)
	}

	return result.OpenAccessPDF.URL, nil
}

// ExtractRefsFromS2Paper extracts arXiv IDs from a Semantic Scholar paper's references.
func ExtractRefsFromS2Paper(paper *S2Paper) []string {
	if paper == nil {
		return nil
	}

	var refs []string
	for _, ref := range paper.References {
		if ref.ExternalIDs.ArXiv != "" {
			refs = append(refs, ref.ExternalIDs.ArXiv)
		}
	}
	return refs
}

// ExtractCitationsFromS2Paper extracts arXiv IDs from a Semantic Scholar paper's citations.
func ExtractCitationsFromS2Paper(paper *S2Paper) []string {
	if paper == nil {
		return nil
	}

	var cites []string
	for _, c := range paper.Citations {
		if c.ExternalIDs.ArXiv != "" {
			cites = append(cites, c.ExternalIDs.ArXiv)
		}
	}
	return cites
}

func (c *S2Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "arxiv-archive/1.0 (https://github.com/timholm/arxiv-archive)")
	if c.apiKey != "" {
		req.Header.Set("x-api-key", c.apiKey)
	}
}

func (c *S2Client) rateLimit() {
	elapsed := time.Since(c.lastReq)
	if elapsed < c.rateDelay {
		time.Sleep(c.rateDelay - elapsed)
	}
	c.lastReq = time.Now()
}

func (c *S2Client) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*2) * time.Second):
			}
			// Clone the request for retry
			req = req.Clone(ctx)
		}

		resp, err = c.client.Do(req)
		if err != nil {
			continue
		}

		// Retry on 429 (rate limited)
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			if retryAfter != "" {
				if d, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(d):
					}
				}
			} else {
				time.Sleep(5 * time.Second)
			}
			continue
		}

		// Retry on 5xx
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			continue
		}

		return resp, nil
	}

	if err != nil {
		return nil, err
	}
	return resp, nil
}
