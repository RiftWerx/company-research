package companyhouse_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/riftwerx/company-research/internal/companyhouse"
)

func TestValidateCHNumber(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid formats
		{"8-digit English", "00445790", true},
		{"Scottish prefix", "SC123456", true},
		{"Northern Ireland prefix", "NI012345", true},
		{"LLP prefix", "OC300001", true},
		{"lowercase accepted", "sc123456", true},
		{"minimum length 6", "AB1234", true},
		{"maximum length 10", "AB12345678", true},
		// Invalid: length
		{"too short (5)", "AB123", false},
		{"too long (11)", "AB123456789", false},
		{"empty", "", false},
		// Invalid: characters that would enable path traversal
		{"path traversal", "../../etc", false},
		{"slash", "1234/5678", false},
		{"dot", "12345.678", false},
		{"space", "1234 5678", false},
		{"null byte", "1234\x005678", false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, companyhouse.ValidateCHNumber(test.input), "ValidateCHNumber(%q)", test.input)
		})
	}
}

func TestValidateDocID(t *testing.T) {
	t.Parallel()

	longValid := strings.Repeat("a", 200)
	longInvalid := strings.Repeat("a", 201)

	cases := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid formats
		{"typical base64url ID", "MzAyNDA3MzA4NmFkaXF6a2N4", true},
		{"minimum length 1", "a", true},
		{"maximum length 200", longValid, true},
		{"all allowed chars", "abc-123_XYZ", true},
		// Invalid: length
		{"empty", "", false},
		{"201 chars (one over limit)", longInvalid, false},
		// Invalid: characters that would enable path traversal
		{"slash", "abc/def", false},
		{"dot-dot slash", "../secret", false},
		{"space", "abc def", false},
		{"equals (base64 padding)", "abc=def", false},
		{"plus (standard base64)", "abc+def", false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, companyhouse.ValidateDocID(test.input), "ValidateDocID(%q)", test.input)
		})
	}
}

func TestValidateDocumentURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		url  string
		want bool
	}{
		// Valid CH document API URLs
		{"https://document-api.company-information.service.gov.uk/document/abc123", true},
		{"https://document-api.company-information.service.gov.uk/document/abc123/content", true},
		// Blocked: relative paths — must be absolute CH API URLs
		{"/document/abc123", false},
		{"/document/abc123/content", false},
		// Blocked: wrong domain
		{"https://evil.com/document/abc123", false},
		{"https://evil.company-information.service.gov.uk/document/abc123", false},
		// Blocked: HTTP (not HTTPS)
		{"http://document-api.company-information.service.gov.uk/document/abc123", false},
		// Blocked: protocol-relative (no scheme but has host)
		{"//document-api.company-information.service.gov.uk/document/abc123", false},
		// Blocked: SSRF to metadata endpoint
		{"http://169.254.169.254/document/foo", false},
	}

	for _, test := range cases {
		t.Run(test.url, func(t *testing.T) {
			t.Parallel()

			// Act
			got := companyhouse.ValidateDocumentURL(test.url)

			// Assert
			assert.Equal(t, test.want, got, "ValidateDocumentURL(%q)", test.url)
		})
	}
}

func TestParseDocumentID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		url    string
		wantID string
		wantOK bool
	}{
		// Content URLs (with /content suffix) — returned by fetch_filing tool input
		{"https://api.company-information.service.gov.uk/document/abc123/content", "abc123", true},
		{"/document/xyz789/content", "xyz789", true},
		// Metadata URLs (without /content) — returned by list_filings / GetFilingHistory
		{"https://document-api.company-information.service.gov.uk/document/MzAxNjM4NjM3NWFkaXF6a2N4", "MzAxNjM4NjM3NWFkaXF6a2N4", true},
		{"/document/abc456", "abc456", true},
		// Invalid inputs
		{"invalid-url-no-document-segment", "", false},
		{"https://example.com/other/path", "", false},
		// Edge case: /document/content — the segment after "document" is the literal
		// word "content", which is an unlikely but valid document ID at the CH API.
		{"/document/content", "content", true},
	}

	for _, test := range cases {
		t.Run(test.url, func(t *testing.T) {
			t.Parallel()

			// Act
			gotID, gotOK := companyhouse.ParseDocumentID(test.url)

			// Assert
			assert.Equal(t, test.wantOK, gotOK, "ParseDocumentID(%q) ok", test.url)
			assert.Equal(t, test.wantID, gotID, "ParseDocumentID(%q) id", test.url)
		})
	}
}
