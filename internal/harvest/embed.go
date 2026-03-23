package harvest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	embeddingModel = "text-embedding-3-small"
	embeddingDim   = 1536
	embedBatchSize = 50
)

// EmbedClient generates embeddings via the llm-router's OpenAI-compatible endpoint.
type EmbedClient struct {
	client    *http.Client
	routerURL string
}

// NewEmbedClient creates a new embedding client pointing at the llm-router.
func NewEmbedClient(routerURL string) *EmbedClient {
	return &EmbedClient{
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		routerURL: routerURL,
	}
}

// embedRequest is the request body for the OpenAI embeddings API.
type embedRequest struct {
	Input interface{} `json:"input"` // string or []string
	Model string      `json:"model"`
}

// embedResponse is the response from the OpenAI embeddings API.
type embedResponse struct {
	Data  []embedData `json:"data"`
	Model string      `json:"model"`
	Usage embedUsage  `json:"usage"`
}

type embedData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embedUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Embed generates an embedding for a single text.
func (c *EmbedClient) Embed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return embeddings[0], nil
}

// EmbedBatch generates embeddings for multiple texts in a single API call.
// Handles chunking into batches of 50 automatically.
func (c *EmbedClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))

	// Process in batches
	for i := 0; i < len(texts); i += embedBatchSize {
		end := i + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		embeddings, err := c.embedBatchRaw(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d:%d]: %w", i, end, err)
		}

		for j, emb := range embeddings {
			results[i+j] = emb
		}
	}

	return results, nil
}

func (c *EmbedClient) embedBatchRaw(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embedRequest{
		Input: texts,
		Model: embeddingModel,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1/embeddings", c.routerURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "arxiv-archive/1.0")

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*2) * time.Second):
			}
			req = req.Clone(ctx)
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		resp, err = c.client.Do(req)
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			continue
		}
		break
	}
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API error (status %d): %s", resp.StatusCode, string(body))
	}

	var embResp embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	// Sort by index to ensure correct ordering
	results := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("invalid embedding index %d", d.Index)
		}
		if len(d.Embedding) != embeddingDim {
			return nil, fmt.Errorf("expected %d-dim embedding, got %d", embeddingDim, len(d.Embedding))
		}
		results[d.Index] = d.Embedding
	}

	return results, nil
}
