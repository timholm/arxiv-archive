// Package config loads and validates configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all application configuration.
type Config struct {
	// PostgresURL is the connection string for PostgreSQL with pgvector.
	PostgresURL string

	// ArxivDataDir is the root directory for storing full-text paper files.
	ArxivDataDir string

	// LLMRouterURL is the base URL for the llm-router embedding endpoint.
	LLMRouterURL string

	// S2APIKey is the optional Semantic Scholar API key for higher rate limits.
	S2APIKey string

	// Categories is the list of arXiv categories to sync.
	Categories []string
}

// DefaultCategories are the arXiv categories synced by default.
var DefaultCategories = []string{
	"cs.AI",
	"cs.CL",
	"cs.LG",
	"cs.SE",
	"cs.CV",
	"stat.ML",
}

// Load reads configuration from environment variables.
// Returns an error if required variables are missing.
func Load() (*Config, error) {
	c := &Config{}

	c.PostgresURL = os.Getenv("POSTGRES_URL")
	if c.PostgresURL == "" {
		return nil, fmt.Errorf("POSTGRES_URL is required")
	}

	c.ArxivDataDir = os.Getenv("ARXIV_DATA_DIR")
	if c.ArxivDataDir == "" {
		c.ArxivDataDir = "/srv/arxiv"
	}

	c.LLMRouterURL = os.Getenv("LLM_ROUTER_URL")

	c.S2APIKey = os.Getenv("S2_API_KEY")

	cats := os.Getenv("ARXIV_CATEGORIES")
	if cats != "" {
		c.Categories = strings.Split(cats, ",")
		for i, cat := range c.Categories {
			c.Categories[i] = strings.TrimSpace(cat)
		}
	} else {
		c.Categories = DefaultCategories
	}

	return c, nil
}

// LoadWithDefaults loads config but uses defaults for missing required fields.
// Useful for commands that don't need all config (e.g., stats, read).
func LoadWithDefaults() *Config {
	c, err := Load()
	if err != nil {
		// Fill in defaults for missing required fields
		c = &Config{
			PostgresURL:  os.Getenv("POSTGRES_URL"),
			ArxivDataDir: "/srv/arxiv",
			LLMRouterURL: os.Getenv("LLM_ROUTER_URL"),
			S2APIKey:     os.Getenv("S2_API_KEY"),
			Categories:   DefaultCategories,
		}
		if dir := os.Getenv("ARXIV_DATA_DIR"); dir != "" {
			c.ArxivDataDir = dir
		}
		cats := os.Getenv("ARXIV_CATEGORIES")
		if cats != "" {
			c.Categories = strings.Split(cats, ",")
			for i, cat := range c.Categories {
				c.Categories[i] = strings.TrimSpace(cat)
			}
		}
	}
	return c
}
