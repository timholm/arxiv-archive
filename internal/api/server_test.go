package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseIntParam(t *testing.T) {
	tests := []struct {
		name       string
		queryParam string
		defaultVal int
		expected   int
	}{
		{"empty", "", 20, 20},
		{"valid", "10", 20, 10},
		{"invalid", "abc", 20, 20},
		{"negative", "-5", 20, 20},
		{"zero", "0", 20, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?limit="+tt.queryParam, nil)
			got := parseIntParam(req, "limit", tt.defaultVal)
			if got != tt.expected {
				t.Errorf("parseIntParam() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestResponseWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, status: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)
	if rw.status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rw.status, http.StatusNotFound)
	}
}
