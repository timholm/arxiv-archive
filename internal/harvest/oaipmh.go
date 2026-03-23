// Package harvest implements data fetching from arXiv (OAI-PMH), Semantic Scholar,
// and the embedding service.
package harvest

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/timholm/arxiv-archive/internal/db"
)

const (
	oaiBaseURL       = "https://export.arxiv.org/oai2"
	oaiPollDelay     = 1 * time.Second // Polite delay between requests
	oaiRetryDelay    = 30 * time.Second
	oaiMaxRetries    = 5
	oaiMetadataPrefix = "arXiv"
)

// OAIPMHClient fetches paper metadata from arXiv via the OAI-PMH protocol.
type OAIPMHClient struct {
	client  *http.Client
	baseURL string
}

// NewOAIPMHClient creates a new OAI-PMH client.
func NewOAIPMHClient() *OAIPMHClient {
	return &OAIPMHClient{
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		baseURL: oaiBaseURL,
	}
}

// OAI-PMH XML response structures

type oaiResponse struct {
	XMLName        xml.Name       `xml:"OAI-PMH"`
	Error          *oaiError      `xml:"error"`
	ListRecords    *listRecords   `xml:"ListRecords"`
}

type oaiError struct {
	Code    string `xml:"code,attr"`
	Message string `xml:",chardata"`
}

type listRecords struct {
	Records         []oaiRecord     `xml:"record"`
	ResumptionToken resumptionToken `xml:"resumptionToken"`
}

type resumptionToken struct {
	Token          string `xml:",chardata"`
	CompleteSize   string `xml:"completeListSize,attr"`
	Cursor         string `xml:"cursor,attr"`
	ExpirationDate string `xml:"expirationDate,attr"`
}

type oaiRecord struct {
	Header   oaiHeader   `xml:"header"`
	Metadata oaiMetadata `xml:"metadata"`
}

type oaiHeader struct {
	Identifier string   `xml:"identifier"`
	Datestamp  string   `xml:"datestamp"`
	SetSpec    []string `xml:"setSpec"`
	Status     string   `xml:"status,attr"`
}

// arXiv-specific metadata format
type oaiMetadata struct {
	ArXiv arXivRecord `xml:"arXiv"`
}

type arXivRecord struct {
	ID         string      `xml:"id"`
	Created    string      `xml:"created"`
	Updated    string      `xml:"updated"`
	Authors    arXivAuthors `xml:"authors"`
	Title      string      `xml:"title"`
	Categories string      `xml:"categories"`
	Abstract   string      `xml:"abstract"`
	DOI        string      `xml:"doi"`
	JournalRef string      `xml:"journal-ref"`
}

type arXivAuthors struct {
	Authors []arXivAuthor `xml:"author"`
}

type arXivAuthor struct {
	Keyname  string `xml:"keyname"`
	Forenames string `xml:"forenames"`
}

// HarvestResult contains the result of a single harvest batch.
type HarvestResult struct {
	Papers          []*db.Paper
	ResumptionToken string
	CompleteSize    string
	Cursor          string
}

// Harvest fetches a batch of records from arXiv via OAI-PMH.
// If resumptionToken is empty, it starts a fresh harvest for the given category.
// If resumptionToken is provided, it continues from where it left off.
func (c *OAIPMHClient) Harvest(ctx context.Context, category string, resumeToken string, from string) (*HarvestResult, error) {
	params := url.Values{}
	params.Set("verb", "ListRecords")

	if resumeToken != "" {
		// When using a resumption token, only pass the token
		params.Set("resumptionToken", resumeToken)
	} else {
		params.Set("metadataPrefix", oaiMetadataPrefix)
		if category != "" {
			params.Set("set", category)
		}
		if from != "" {
			params.Set("from", from)
		}
	}

	reqURL := fmt.Sprintf("%s?%s", c.baseURL, params.Encode())

	var resp *http.Response
	var err error
	for attempt := 0; attempt <= oaiMaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff for retries
			delay := oaiRetryDelay * time.Duration(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("User-Agent", "arxiv-archive/1.0 (https://github.com/timholm/arxiv-archive)")

		resp, err = c.client.Do(req)
		if err != nil {
			continue
		}

		// Handle 503 Retry-After (arXiv rate limiting)
		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			retryAfter := resp.Header.Get("Retry-After")
			if retryAfter != "" {
				if d, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(d):
					}
					continue
				}
			}
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break
		}
		resp.Body.Close()
		err = fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch OAI-PMH records after %d retries: %w", oaiMaxRetries, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var oai oaiResponse
	if err := xml.Unmarshal(body, &oai); err != nil {
		return nil, fmt.Errorf("parse OAI-PMH XML: %w", err)
	}

	if oai.Error != nil {
		if oai.Error.Code == "noRecordsMatch" {
			return &HarvestResult{}, nil
		}
		return nil, fmt.Errorf("OAI-PMH error [%s]: %s", oai.Error.Code, oai.Error.Message)
	}

	if oai.ListRecords == nil {
		return &HarvestResult{}, nil
	}

	result := &HarvestResult{
		ResumptionToken: strings.TrimSpace(oai.ListRecords.ResumptionToken.Token),
		CompleteSize:    oai.ListRecords.ResumptionToken.CompleteSize,
		Cursor:          oai.ListRecords.ResumptionToken.Cursor,
	}

	for _, rec := range oai.ListRecords.Records {
		// Skip deleted records
		if rec.Header.Status == "deleted" {
			continue
		}

		paper := recordToPaper(rec)
		if paper != nil {
			result.Papers = append(result.Papers, paper)
		}
	}

	return result, nil
}

// recordToPaper converts an OAI-PMH record to a db.Paper.
func recordToPaper(rec oaiRecord) *db.Paper {
	arxiv := rec.Metadata.ArXiv
	if arxiv.ID == "" {
		// Fall back to header identifier
		arxiv.ID = db.NormalizeArxivID(rec.Header.Identifier)
	}
	if arxiv.ID == "" {
		return nil
	}

	p := &db.Paper{
		ArxivID:    arxiv.ID,
		Title:      cleanText(arxiv.Title),
		Abstract:   cleanText(arxiv.Abstract),
		Categories: arxiv.Categories,
		DOI:        arxiv.DOI,
		JournalRef: arxiv.JournalRef,
	}

	// Parse authors
	var authors []string
	for _, a := range arxiv.Authors.Authors {
		name := strings.TrimSpace(a.Forenames + " " + a.Keyname)
		if name != "" {
			authors = append(authors, name)
		}
	}
	if len(authors) > 0 {
		p.Authors = strings.Join(authors, ", ")
	}

	// Parse dates
	if arxiv.Created != "" {
		if t, err := parseArxivDate(arxiv.Created); err == nil {
			p.Published = &t
		}
	}
	if arxiv.Updated != "" {
		if t, err := parseArxivDate(arxiv.Updated); err == nil {
			p.Updated = &t
		}
	}

	return p
}

// parseArxivDate parses dates in arXiv format (YYYY-MM-DD or YYYY-MM-DDTHH:MM:SSZ).
func parseArxivDate(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date: %s", s)
}

// cleanText normalizes whitespace in text fields.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	// Collapse multiple whitespace
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}
