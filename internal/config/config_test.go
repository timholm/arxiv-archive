package config

import (
	"os"
	"testing"
)

func TestLoad_RequiresPostgresURL(t *testing.T) {
	os.Unsetenv("POSTGRES_URL")
	_, err := Load()
	if err == nil {
		t.Error("expected error when POSTGRES_URL is not set")
	}
}

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("POSTGRES_URL", "postgres://localhost/test")
	defer os.Unsetenv("POSTGRES_URL")
	os.Unsetenv("ARXIV_DATA_DIR")
	os.Unsetenv("ARXIV_CATEGORIES")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ArxivDataDir != "/srv/arxiv" {
		t.Errorf("ArxivDataDir = %q, want /srv/arxiv", cfg.ArxivDataDir)
	}

	if len(cfg.Categories) != len(DefaultCategories) {
		t.Errorf("Categories length = %d, want %d", len(cfg.Categories), len(DefaultCategories))
	}
}

func TestLoad_CustomCategories(t *testing.T) {
	os.Setenv("POSTGRES_URL", "postgres://localhost/test")
	os.Setenv("ARXIV_CATEGORIES", "cs.AI, cs.CL")
	defer func() {
		os.Unsetenv("POSTGRES_URL")
		os.Unsetenv("ARXIV_CATEGORIES")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Categories) != 2 {
		t.Fatalf("Categories length = %d, want 2", len(cfg.Categories))
	}
	if cfg.Categories[0] != "cs.AI" {
		t.Errorf("Categories[0] = %q, want cs.AI", cfg.Categories[0])
	}
	if cfg.Categories[1] != "cs.CL" {
		t.Errorf("Categories[1] = %q, want cs.CL", cfg.Categories[1])
	}
}

func TestLoad_CustomDataDir(t *testing.T) {
	os.Setenv("POSTGRES_URL", "postgres://localhost/test")
	os.Setenv("ARXIV_DATA_DIR", "/tmp/arxiv")
	defer func() {
		os.Unsetenv("POSTGRES_URL")
		os.Unsetenv("ARXIV_DATA_DIR")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ArxivDataDir != "/tmp/arxiv" {
		t.Errorf("ArxivDataDir = %q, want /tmp/arxiv", cfg.ArxivDataDir)
	}
}

func TestLoadWithDefaults(t *testing.T) {
	os.Unsetenv("POSTGRES_URL")

	cfg := LoadWithDefaults()
	if cfg == nil {
		t.Fatal("LoadWithDefaults returned nil")
	}

	if cfg.ArxivDataDir != "/srv/arxiv" {
		t.Errorf("ArxivDataDir = %q, want /srv/arxiv", cfg.ArxivDataDir)
	}

	if len(cfg.Categories) != len(DefaultCategories) {
		t.Errorf("Categories length = %d, want %d", len(cfg.Categories), len(DefaultCategories))
	}
}
