package harvest

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCleanText(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"  hello  world  ", "hello world"},
		{"line1\nline2\nline3", "line1 line2 line3"},
		{"tabs\there\tand\tthere", "tabs here and there"},
		{"multiple   spaces", "multiple spaces"},
		{"  \n\t  clean me  \n\t  ", "clean me"},
		{"", ""},
		{"no change", "no change"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := cleanText(tt.input)
			if got != tt.expected {
				t.Errorf("cleanText(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseArxivDate(t *testing.T) {
	tests := []struct {
		input   string
		year    int
		month   int
		wantErr bool
	}{
		{"2024-01-15", 2024, 1, false},
		{"2024-01-15T10:30:00Z", 2024, 1, false},
		{"not-a-date", 0, 0, true},
		{"", 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseArxivDate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseArxivDate(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Year() != tt.year {
					t.Errorf("year = %d, want %d", got.Year(), tt.year)
				}
				if int(got.Month()) != tt.month {
					t.Errorf("month = %d, want %d", got.Month(), tt.month)
				}
			}
		})
	}
}

func TestRecordToPaper(t *testing.T) {
	rec := oaiRecord{
		Header: oaiHeader{
			Identifier: "oai:arXiv.org:2301.12345",
			Datestamp:   "2024-01-15",
		},
		Metadata: oaiMetadata{
			ArXiv: arXivRecord{
				ID:         "2301.12345",
				Title:      "Test Paper Title",
				Abstract:   "This is a test abstract.",
				Categories: "cs.AI cs.CL",
				Created:    "2024-01-15",
				Authors: arXivAuthors{
					Authors: []arXivAuthor{
						{Forenames: "John", Keyname: "Doe"},
						{Forenames: "Jane", Keyname: "Smith"},
					},
				},
				DOI: "10.1234/test",
			},
		},
	}

	paper := recordToPaper(rec)
	if paper == nil {
		t.Fatal("recordToPaper returned nil")
	}

	if paper.ArxivID != "2301.12345" {
		t.Errorf("ArxivID = %q, want %q", paper.ArxivID, "2301.12345")
	}
	if paper.Title != "Test Paper Title" {
		t.Errorf("Title = %q, want %q", paper.Title, "Test Paper Title")
	}
	if paper.Abstract != "This is a test abstract." {
		t.Errorf("Abstract = %q, want %q", paper.Abstract, "This is a test abstract.")
	}
	if paper.Authors != "John Doe, Jane Smith" {
		t.Errorf("Authors = %q, want %q", paper.Authors, "John Doe, Jane Smith")
	}
	if paper.DOI != "10.1234/test" {
		t.Errorf("DOI = %q, want %q", paper.DOI, "10.1234/test")
	}
	if paper.Published == nil {
		t.Error("Published is nil")
	}
}

func TestRecordToPaper_Empty(t *testing.T) {
	rec := oaiRecord{}
	paper := recordToPaper(rec)
	if paper != nil {
		t.Error("expected nil for empty record")
	}
}

func TestRecordToPaper_Deleted(t *testing.T) {
	rec := oaiRecord{
		Header: oaiHeader{
			Status: "deleted",
		},
	}
	// Deleted records should be filtered before calling recordToPaper
	// but the function handles empty IDs gracefully
	paper := recordToPaper(rec)
	if paper != nil {
		t.Error("expected nil for deleted record with no ID")
	}
}

func TestOAIXMLParsing(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <ListRecords>
    <record>
      <header>
        <identifier>oai:arXiv.org:2301.12345</identifier>
        <datestamp>2024-01-15</datestamp>
        <setSpec>cs</setSpec>
      </header>
      <metadata>
        <arXiv xmlns="http://arxiv.org/OAI/arXiv/">
          <id>2301.12345</id>
          <created>2024-01-15</created>
          <authors>
            <author>
              <keyname>Doe</keyname>
              <forenames>John</forenames>
            </author>
          </authors>
          <title>A Great Paper</title>
          <categories>cs.AI</categories>
          <abstract>An abstract.</abstract>
        </arXiv>
      </metadata>
    </record>
    <resumptionToken cursor="0" completeListSize="100">token123</resumptionToken>
  </ListRecords>
</OAI-PMH>`

	var oai oaiResponse
	if err := xml.Unmarshal([]byte(xmlData), &oai); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if oai.ListRecords == nil {
		t.Fatal("ListRecords is nil")
	}
	if len(oai.ListRecords.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(oai.ListRecords.Records))
	}
	if oai.ListRecords.ResumptionToken.Token != "token123" {
		t.Errorf("resumption token = %q, want %q", oai.ListRecords.ResumptionToken.Token, "token123")
	}
	if oai.ListRecords.ResumptionToken.CompleteSize != "100" {
		t.Errorf("complete size = %q, want %q", oai.ListRecords.ResumptionToken.CompleteSize, "100")
	}
}

func TestOAIPMHClient_Harvest_Mock(t *testing.T) {
	xmlResp := `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <ListRecords>
    <record>
      <header>
        <identifier>oai:arXiv.org:2301.00001</identifier>
        <datestamp>2024-01-01</datestamp>
      </header>
      <metadata>
        <arXiv xmlns="http://arxiv.org/OAI/arXiv/">
          <id>2301.00001</id>
          <created>2024-01-01</created>
          <authors><author><keyname>Test</keyname><forenames>Author</forenames></author></authors>
          <title>Mock Paper</title>
          <categories>cs.AI</categories>
          <abstract>Mock abstract.</abstract>
        </arXiv>
      </metadata>
    </record>
    <resumptionToken cursor="0" completeListSize="1"></resumptionToken>
  </ListRecords>
</OAI-PMH>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(xmlResp))
	}))
	defer server.Close()

	client := &OAIPMHClient{
		client:  server.Client(),
		baseURL: server.URL,
	}

	result, err := client.Harvest(context.Background(), "cs.AI", "", "")
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	if len(result.Papers) != 1 {
		t.Fatalf("expected 1 paper, got %d", len(result.Papers))
	}

	if result.Papers[0].ArxivID != "2301.00001" {
		t.Errorf("ArxivID = %q, want %q", result.Papers[0].ArxivID, "2301.00001")
	}
	if result.Papers[0].Title != "Mock Paper" {
		t.Errorf("Title = %q, want %q", result.Papers[0].Title, "Mock Paper")
	}

	// Empty resumption token means we're done
	if result.ResumptionToken != "" {
		t.Errorf("ResumptionToken = %q, want empty", result.ResumptionToken)
	}
}

func TestOAIPMHClient_NoRecordsMatch(t *testing.T) {
	xmlResp := `<?xml version="1.0" encoding="UTF-8"?>
<OAI-PMH xmlns="http://www.openarchives.org/OAI/2.0/">
  <error code="noRecordsMatch">No records match the request</error>
</OAI-PMH>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(xmlResp))
	}))
	defer server.Close()

	client := &OAIPMHClient{
		client:  server.Client(),
		baseURL: server.URL,
	}

	result, err := client.Harvest(context.Background(), "cs.AI", "", "2099-01-01")
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	if len(result.Papers) != 0 {
		t.Errorf("expected 0 papers, got %d", len(result.Papers))
	}
}
