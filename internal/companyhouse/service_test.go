package companyhouse_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/riftwerx/company-research/internal/companyhouse"
)

// stubHTTP is a test double for HTTPDoer that delegates to a provided function.
type stubHTTP struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s *stubHTTP) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	return s.fn(req)
}

// jsonResponse builds an *http.Response with a JSON body and the given status code.
func jsonResponse(status int, body any) *http.Response {
	b, err := json.Marshal(body)
	if err != nil {
		panic("jsonResponse: json.Marshal: " + err.Error())
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(b))),
	}
}

// statusResponse builds an *http.Response with an empty body and the given status code.
func statusResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// rateLimitedResponse builds a 429 response with Retry-After: 0 for fast tests.
func rateLimitedResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"0"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// ---- SearchCompanies ----

func TestSearchCompanies(t *testing.T) {
	t.Run("should return parsed results for a successful response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{
				"items": []map[string]any{
					{
						"company_number":   "00445790",
						"title":            "TESCO PLC",
						"company_type":     "plc",
						"company_status":   "active",
						"date_of_creation": "1947-11-27",
						"registered_office_address": map[string]any{
							"address_line_1": "Tesco House",
							"locality":       "Welwyn Garden City",
							"postal_code":    "AL7 1GA",
						},
					},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		results, err := svc.SearchCompanies(context.Background(), "Tesco", 1)

		// Assert
		require.NoError(t, err)
		assert.Len(t, results, 1)
		assert.Equal(t, "00445790", results[0].CompanyNumber)
		assert.Equal(t, "TESCO PLC", results[0].Title)
	})

	t.Run("should set Basic Auth with the API key as the username", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var gotUser, gotPass string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotUser, gotPass, _ = req.BasicAuth()
			return jsonResponse(http.StatusOK, map[string]any{"items": []any{}}), nil
		}}
		svc := companyhouse.NewForTest(stub, "my-api-key", "")

		// Act
		_, err := svc.SearchCompanies(context.Background(), "test", 0)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "my-api-key", gotUser)
		assert.Empty(t, gotPass)
	})

	t.Run("should return ErrUnauthorized for a 401 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusUnauthorized), nil
		}}
		svc := companyhouse.NewForTest(stub, "bad-key", "")

		// Act
		_, err := svc.SearchCompanies(context.Background(), "test", 0)

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrUnauthorized)
	})

	t.Run("should return ErrRateLimited when all retries are exhausted", func(t *testing.T) {
		t.Parallel()

		// Arrange — return 429 on both the initial request and the retry
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return rateLimitedResponse(), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.SearchCompanies(context.Background(), "test", 0)

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrRateLimited)
	})

	t.Run("should retry once on a 429 and succeed", func(t *testing.T) {
		t.Parallel()

		// Arrange — first call returns 429, second returns success
		callCount := 0
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return rateLimitedResponse(), nil
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"items": []map[string]any{
					{"company_number": "00445790", "title": "TESCO PLC"},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		results, err := svc.SearchCompanies(context.Background(), "Tesco", 1)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
		assert.Len(t, results, 1)
		assert.Equal(t, "TESCO PLC", results[0].Title)
	})
}

// ---- GetCompanyProfile ----

func TestGetCompanyProfile(t *testing.T) {
	t.Run("should zero-pad the company number to 8 digits in the request path", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var gotPath string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return jsonResponse(http.StatusOK, map[string]any{
				"company_number": "00012345",
				"company_name":   "EXAMPLE LTD",
				"type":           "ltd",
				"company_status": "active",
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		profile, err := svc.GetCompanyProfile(context.Background(), "12345")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "/company/00012345", gotPath)
		assert.Equal(t, "EXAMPLE LTD", profile.CompanyName)
	})

	t.Run("should return ErrNotFound for a 404 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusNotFound), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetCompanyProfile(context.Background(), "99999999")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrNotFound)
	})
}

// ---- GetFilingHistory ----

func TestGetFilingHistory(t *testing.T) {
	t.Run("should parse filing dates and document URLs", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{
				"items": []map[string]any{
					{
						"transaction_id": "MzAxNjM4NjM3NWFkaXF6a2N4",
						"type":           "AA",
						"description":    "accounts-with-accounts-type-full",
						"date":           "2024-06-21",
						"links": map[string]any{
							"document_metadata": "/document/MzAxNjM4NjM3NWFkaXF6a2N4",
						},
					},
					{
						"transaction_id": "abc123",
						"type":           "CS01",
						"description":    "confirmation-statement-with-no-updates",
						"date":           "2024-01-15",
						"links":          map[string]any{},
					},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		filings, err := svc.GetFilingHistory(context.Background(), "00445790", companyhouse.ListFilingsOptions{})

		// Assert
		require.NoError(t, err)
		assert.Len(t, filings, 2)
		wantDate := time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC)
		assert.True(t, filings[0].Date.Equal(wantDate), "date: got %v, want %v", filings[0].Date, wantDate)
		assert.Equal(t, "/document/MzAxNjM4NjM3NWFkaXF6a2N4", filings[0].DocumentURL)
	})
}

// ---- GetDocument ----

func TestGetDocument(t *testing.T) {
	t.Run("should retry once on a 429 and succeed", func(t *testing.T) {
		t.Parallel()

		// Arrange — first call returns 429 with Retry-After: 0, second returns success
		callCount := 0
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			callCount++
			if callCount == 1 {
				return rateLimitedResponse(), nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/pdf"}},
				Body:       io.NopCloser(strings.NewReader("%PDF")),
			}, nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		doc, err := svc.GetDocument(context.Background(), "https://document-api.company-information.service.gov.uk/document/abc123")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
		if doc != nil {
			doc.Body.Close()
		}
	})

	t.Run("should return ErrRateLimited when all retries are exhausted", func(t *testing.T) {
		t.Parallel()

		// Arrange — both calls return 429
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return rateLimitedResponse(), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetDocument(context.Background(), "https://document-api.company-information.service.gov.uk/document/abc123")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrRateLimited)
	})

	t.Run("should return the body and content type, appending /content to the URL", func(t *testing.T) {
		t.Parallel()

		// Arrange
		pdfContent := "%PDF-1.4 fake content"
		var gotURL, gotAccept string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			gotAccept = req.Header.Get("Accept")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/pdf"}},
				Body:       io.NopCloser(strings.NewReader(pdfContent)),
			}, nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act — documentURL is the metadata URL; GetDocument appends /content
		doc, err := svc.GetDocument(context.Background(), "https://document-api.company-information.service.gov.uk/document/abc123")

		// Assert
		require.NoError(t, err)
		if doc != nil {
			defer doc.Body.Close()
			assert.Equal(t, "https://document-api.company-information.service.gov.uk/document/abc123/content", gotURL)
			assert.Equal(t, "application/xhtml+xml,application/pdf,*/*", gotAccept)
			assert.Equal(t, "application/pdf", doc.ContentType)
			b, err := io.ReadAll(doc.Body)
			require.NoError(t, err)
			assert.Equal(t, pdfContent, string(b))
		}
	})

	t.Run("should return ErrNotFound for a 404 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusNotFound), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetDocument(context.Background(), "https://document-api.company-information.service.gov.uk/document/missing")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrNotFound)
	})
}
