package harvest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedClient_Embed_Mock(t *testing.T) {
	// Create a mock embedding server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected /v1/embeddings, got %s", r.URL.Path)
		}

		// Decode request
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if req.Model != embeddingModel {
			t.Errorf("model = %q, want %q", req.Model, embeddingModel)
		}

		// Generate fake embedding
		embedding := make([]float32, embeddingDim)
		for i := range embedding {
			embedding[i] = float32(i) / float32(embeddingDim)
		}

		resp := embedResponse{
			Data: []embedData{
				{Embedding: embedding, Index: 0},
			},
			Model: embeddingModel,
			Usage: embedUsage{PromptTokens: 10, TotalTokens: 10},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewEmbedClient(server.URL)
	embedding, err := client.Embed(context.Background(), "test text")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(embedding) != embeddingDim {
		t.Errorf("embedding length = %d, want %d", len(embedding), embeddingDim)
	}
}

func TestEmbedClient_EmbedBatch_Mock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		// Get the input texts
		texts, ok := req.Input.([]interface{})
		if !ok {
			t.Fatal("expected array input")
		}

		// Generate fake embeddings for each text
		var data []embedData
		for i := range texts {
			embedding := make([]float32, embeddingDim)
			for j := range embedding {
				embedding[j] = float32(i*embeddingDim+j) / float32(embeddingDim*len(texts))
			}
			data = append(data, embedData{Embedding: embedding, Index: i})
		}

		resp := embedResponse{
			Data:  data,
			Model: embeddingModel,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewEmbedClient(server.URL)
	texts := []string{"text one", "text two", "text three"}

	embeddings, err := client.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}

	if len(embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(embeddings))
	}

	for i, emb := range embeddings {
		if len(emb) != embeddingDim {
			t.Errorf("embedding[%d] length = %d, want %d", i, len(emb), embeddingDim)
		}
	}
}

func TestEmbedClient_EmbedBatch_Empty(t *testing.T) {
	client := NewEmbedClient("http://localhost:9999")
	embeddings, err := client.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if embeddings != nil {
		t.Errorf("expected nil, got %v", embeddings)
	}
}

func TestEmbedClient_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	client := NewEmbedClient(server.URL)
	_, err := client.Embed(context.Background(), "test")
	if err == nil {
		t.Error("expected error for server error response")
	}
}
