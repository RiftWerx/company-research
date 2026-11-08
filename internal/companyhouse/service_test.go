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

// ---- GetCharges ----

func TestGetCharges(t *testing.T) {
	t.Run("should parse the classification, status, and persons entitled", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var gotPath string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return jsonResponse(http.StatusOK, map[string]any{
				"total_count": 1,
				"items": []map[string]any{
					{
						"classification": map[string]any{
							"type":        "charge-description",
							"description": "Legal charge",
						},
						"status":       "outstanding",
						"delivered_on": "2009-11-06",
						"persons_entitled": []map[string]any{
							{"name": "Example Bank PLC"},
						},
					},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		charges, err := svc.GetCharges(context.Background(), "00445790")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "/company/00445790/charges", gotPath)
		assert.Equal(t, []companyhouse.Charge{{
			Classification:  companyhouse.ChargeClassification{Description: "Legal charge", Type: "charge-description"},
			Status:          "outstanding",
			DeliveredOn:     "2009-11-06",
			PersonsEntitled: []string{"Example Bank PLC"},
		}}, charges)
	})

	t.Run("should retrieve every page", func(t *testing.T) {
		t.Parallel()

		var gotStarts []string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotStarts = append(gotStarts, req.URL.Query().Get("start_index"))
			assert.Equal(t, "100", req.URL.Query().Get("items_per_page"))
			switch req.URL.Query().Get("start_index") {
			case "0":
				return jsonResponse(http.StatusOK, map[string]any{
					"total_count": 3,
					"items": []map[string]any{
						{"classification": map[string]any{"description": "First", "type": "charge-description"}, "status": "outstanding"},
						{"classification": map[string]any{"description": "Second", "type": "charge-description"}, "status": "outstanding"},
					},
				}), nil
			case "2":
				return jsonResponse(http.StatusOK, map[string]any{
					"total_count": 3,
					"items":       []map[string]any{{"classification": map[string]any{"description": "Third", "type": "charge-description"}, "status": "outstanding"}},
				}), nil
			default:
				t.Fatalf("unexpected start_index %q", req.URL.Query().Get("start_index"))
				return nil, nil
			}
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		charges, err := svc.GetCharges(context.Background(), "00445790")

		require.NoError(t, err)
		assert.Equal(t, []string{"0", "2"}, gotStarts)
		require.Len(t, charges, 3)
		assert.Equal(t, companyhouse.ChargeClassification{Description: "First", Type: "charge-description"}, charges[0].Classification)
		assert.Equal(t, companyhouse.ChargeClassification{Description: "Third", Type: "charge-description"}, charges[2].Classification)
	})

	t.Run("should reject a register larger than the safety limit", func(t *testing.T) {
		t.Parallel()

		stub := &stubHTTP{fn: func(_ *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{"total_count": 10_001, "items": []any{}}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		_, err := svc.GetCharges(context.Background(), "00445790")

		assert.ErrorContains(t, err, "exceeding safety limit")
	})

	t.Run("should reject changing pagination totals", func(t *testing.T) {
		t.Parallel()

		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("start_index") == "0" {
				return jsonResponse(http.StatusOK, map[string]any{
					"total_count": 2,
					"items":       []map[string]any{{"status": "outstanding"}},
				}), nil
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"total_count": 3,
				"items":       []map[string]any{{"status": "outstanding"}},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		_, err := svc.GetCharges(context.Background(), "00445790")

		assert.ErrorContains(t, err, "total_count changed")
	})

	t.Run("should return ErrNotFound for a 404 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusNotFound), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetCharges(context.Background(), "00000001")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrNotFound)
	})
}

// ---- GetPersonsWithSignificantControl ----

func TestGetPersonsWithSignificantControl(t *testing.T) {
	t.Run("should parse name, kind, natures of control, and ceased status", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var gotPath string
		gotQuery := map[string]string{}
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			for key := range req.URL.Query() {
				gotQuery[key] = req.URL.Query().Get(key)
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"start_index":    0,
				"items_per_page": 100,
				"total_results":  3,
				"items": []map[string]any{
					{
						"name":               "Fresh Trading Limited",
						"kind":               "corporate-entity-person-with-significant-control",
						"natures_of_control": []string{"ownership-of-shares-75-to-100-percent"},
						"notified_on":        "2016-04-06",
					},
					{
						"name":        "Jane Doe",
						"kind":        "individual-person-with-significant-control",
						"notified_on": "2016-04-06",
						"ceased_on":   "2023-08-03",
					},
					{"name": "Protected PSC", "kind": "super-secure-person-with-significant-control", "ceased": true},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		pscs, err := svc.GetPersonsWithSignificantControl(context.Background(), "04007092")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "/company/04007092/persons-with-significant-control", gotPath)
		assert.Equal(t, "100", gotQuery["items_per_page"])
		assert.Equal(t, "0", gotQuery["start_index"])
		assert.Equal(t, "false", gotQuery["register_view"])
		require.Len(t, pscs, 3)
		assert.Equal(t, "Fresh Trading Limited", pscs[0].Name)
		assert.False(t, pscs[0].Ceased)
		assert.Empty(t, pscs[0].CeasedOn)
		assert.Equal(t, "2023-08-03", pscs[1].CeasedOn)
		assert.True(t, pscs[2].Ceased)
	})

	t.Run("should retrieve every page", func(t *testing.T) {
		t.Parallel()

		var gotStarts []string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotStarts = append(gotStarts, req.URL.Query().Get("start_index"))
			switch req.URL.Query().Get("start_index") {
			case "0":
				return jsonResponse(http.StatusOK, map[string]any{
					"start_index": 0, "items_per_page": 1, "total_results": 2,
					"items": []map[string]any{{"name": "First PSC"}},
				}), nil
			case "1":
				return jsonResponse(http.StatusOK, map[string]any{
					"start_index": 1, "items_per_page": 1, "total_results": 2,
					"items": []map[string]any{{"name": "Second PSC"}},
				}), nil
			default:
				t.Fatalf("unexpected start_index %q", req.URL.Query().Get("start_index"))
				return nil, nil
			}
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		pscs, err := svc.GetPersonsWithSignificantControl(context.Background(), "04007092")

		require.NoError(t, err)
		assert.Equal(t, []string{"0", "1"}, gotStarts)
		require.Len(t, pscs, 2)
		assert.Equal(t, "Second PSC", pscs[1].Name)
	})

	t.Run("should reject a register larger than the safety limit", func(t *testing.T) {
		t.Parallel()

		stub := &stubHTTP{fn: func(_ *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{
				"start_index": 0, "items_per_page": 100, "total_results": 10_001, "items": []any{},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		_, err := svc.GetPersonsWithSignificantControl(context.Background(), "04007092")

		assert.ErrorContains(t, err, "exceeding safety limit")
	})

	t.Run("should reject changing pagination totals", func(t *testing.T) {
		t.Parallel()

		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			startIndex := req.URL.Query().Get("start_index")
			if startIndex == "0" {
				return jsonResponse(http.StatusOK, map[string]any{
					"start_index": 0, "items_per_page": 1, "total_results": 2,
					"items": []map[string]any{{"name": "First PSC"}},
				}), nil
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"start_index": 1, "items_per_page": 1, "total_results": 3,
				"items": []map[string]any{{"name": "Second PSC"}},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		_, err := svc.GetPersonsWithSignificantControl(context.Background(), "04007092")

		assert.ErrorContains(t, err, "total_results changed")
	})

	t.Run("should return ErrNotFound for a 404 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusNotFound), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetPersonsWithSignificantControl(context.Background(), "00000001")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrNotFound)
	})
}

// ---- GetInsolvencyCases ----

func TestGetInsolvencyCases(t *testing.T) {
	t.Run("should parse case type, number, and dates", func(t *testing.T) {
		t.Parallel()

		// Arrange
		var gotPath string
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			return jsonResponse(http.StatusOK, map[string]any{
				"cases": []map[string]any{
					{
						"type":   "compulsory-liquidation",
						"number": "1",
						"dates": []map[string]any{
							{"type": "petitioned-on", "date": "2018-01-15"},
							{"type": "wound-up-on", "date": "2018-01-15"},
						},
					},
				},
			}), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		cases, err := svc.GetInsolvencyCases(context.Background(), "03782379")

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "/company/03782379/insolvency", gotPath)
		require.Len(t, cases, 1)
		assert.Equal(t, "compulsory-liquidation", cases[0].CaseType)
		assert.Equal(t, "1", cases[0].Number)
		require.Len(t, cases[0].Dates, 2)
		assert.Equal(t, "petitioned-on", cases[0].Dates[0].Type)
		assert.Equal(t, "2018-01-15", cases[0].Dates[0].Date)
	})

	t.Run("should return ErrNotFound for a 404 response", func(t *testing.T) {
		t.Parallel()

		// Arrange
		stub := &stubHTTP{fn: func(req *http.Request) (*http.Response, error) {
			return statusResponse(http.StatusNotFound), nil
		}}
		svc := companyhouse.NewForTest(stub, "test-key", "")

		// Act
		_, err := svc.GetInsolvencyCases(context.Background(), "00000001")

		// Assert
		assert.ErrorIs(t, err, companyhouse.ErrNotFound)
	})
}
