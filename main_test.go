package main

import (
	"testing"
)

func TestLooksLikeArxivID(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"2301.12345", true},
		{"2301.123456", true},
		{"hep-th/9901001", true},
		{"cs/0112017", true},
		{"large language models", false},
		{"transformer architecture attention", false},
		{"", false},
		{"  ", false},
		{"single_word", false},
		{"2301.12345v2", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeArxivID(tt.input)
			if got != tt.expected {
				t.Errorf("looksLikeArxivID(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}
