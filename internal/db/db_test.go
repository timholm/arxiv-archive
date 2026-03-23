package db

import (
	"testing"
)

func TestNormalizeArxivID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"oai:arXiv.org:2301.12345", "2301.12345"},
		{"oai:arXiv.org:2301.12345v1", "2301.12345"},
		{"oai:arXiv.org:2301.12345v2", "2301.12345"},
		{"2301.12345v1", "2301.12345"},
		{"2301.12345v3", "2301.12345"},
		{"2301.12345", "2301.12345"},
		{"hep-th/9901001v1", "hep-th/9901001"},
		{"hep-th/9901001", "hep-th/9901001"},
		{"oai:arXiv.org:hep-th/9901001v2", "hep-th/9901001"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizeArxivID(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeArxivID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		n        int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.n)
			if got != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.expected)
			}
		})
	}
}
