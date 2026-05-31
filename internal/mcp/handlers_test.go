package mcp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/riftwerx/company-research/internal/archive"
	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
)

// mockCHService is a testify mock for CompanyHouseService.
type mockCHService struct {
	mock.Mock
}

func (m *mockCHService) SearchCompanies(ctx context.Context, query string, limit int) ([]companyhouse.SearchResult, error) {
	args := m.Called(ctx, query, limit)
	results, _ := args.Get(0).([]companyhouse.SearchResult)
	return results, args.Error(1)
}

func (m *mockCHService) GetCompanyProfile(ctx context.Context, number string) (*companyhouse.CompanyProfile, error) {
	args := m.Called(ctx, number)
	profile, _ := args.Get(0).(*companyhouse.CompanyProfile)
	return profile, args.Error(1)
}

func (m *mockCHService) GetFilingHistory(ctx context.Context, number string, opts companyhouse.ListFilingsOptions) ([]companyhouse.Filing, error) {
	args := m.Called(ctx, number, opts)
	filings, _ := args.Get(0).([]companyhouse.Filing)
	return filings, args.Error(1)
}

func (m *mockCHService) GetDocument(ctx context.Context, url string) (*companyhouse.Document, error) {
	args := m.Called(ctx, url)
	doc, _ := args.Get(0).(*companyhouse.Document)
	return doc, args.Error(1)
}

// mockFilingCache is a testify mock for FilingCache.
type mockFilingCache struct {
	mock.Mock
}

func (m *mockFilingCache) Get(ctx context.Context, chNumber, docID string) (*cache.FilingEntry, error) {
	args := m.Called(ctx, chNumber, docID)
	entry, _ := args.Get(0).(*cache.FilingEntry)
	return entry, args.Error(1)
}

func (m *mockFilingCache) Put(ctx context.Context, chNumber, docID, contentType, filename string, body io.Reader) (string, int64, error) {
	args := m.Called(ctx, chNumber, docID, contentType, filename, body)
	localPath, _ := args.Get(0).(string)
	written, _ := args.Get(1).(int64)
	return localPath, written, args.Error(2)
}

func (m *mockFilingCache) Clear(ctx context.Context, chNumber string) (cache.ClearResult, error) {
	args := m.Called(ctx, chNumber)
	result, _ := args.Get(0).(cache.ClearResult)
	return result, args.Error(1)
}

func (m *mockFilingCache) PutZipEntries(ctx context.Context, chNumber, docID string, entries []cache.ZipCacheEntry, totalInArchive int) (string, error) {
	args := m.Called(ctx, chNumber, docID, entries, totalInArchive)
	return args.String(0), args.Error(1)
}

func (m *mockFilingCache) GetZipEntries(ctx context.Context, chNumber, docID string) ([]cache.ZipEntryRecord, int, error) {
	args := m.Called(ctx, chNumber, docID)
	records, _ := args.Get(0).([]cache.ZipEntryRecord)
	total, _ := args.Get(1).(int)
	return records, total, args.Error(2)
}

func (m *mockFilingCache) ParseFilingPath(realPath string) (string, string, error) {
	args := m.Called(realPath)
	return args.String(0), args.String(1), args.Error(2)
}

func (m *mockFilingCache) StoreFilingRef(ctx context.Context, chNumber, transactionID, documentURL string) (string, error) {
	args := m.Called(ctx, chNumber, transactionID, documentURL)
	return args.String(0), args.Error(1)
}

func (m *mockFilingCache) ResolveFilingRef(ctx context.Context, chNumber, documentID string) (string, error) {
	args := m.Called(ctx, chNumber, documentID)
	return args.String(0), args.Error(1)
}

func (m *mockFilingCache) ValidatePath(path string) (string, error) {
	args := m.Called(path)
	return args.String(0), args.Error(1)
}

// buildZip creates an in-memory zip archive from a slice of [name, content] pairs.
func buildZip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := w.Create(f[0])
		require.NoError(t, err, "buildZip: create %q", f[0])
		_, err = fw.Write([]byte(f[1]))
		require.NoError(t, err, "buildZip: write %q", f[0])
	}
	require.NoError(t, w.Close(), "buildZip: close")
	return buf.Bytes()
}

// callTool is a test helper that calls the given handler with the provided arguments.
func callTool(handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	return handler(context.Background(), req)
}

// isToolError returns true if the result is a tool error result.
func isToolError(r *mcp.CallToolResult) bool {
	if r == nil {
		return false
	}
	return r.IsError
}

// resultText extracts the text payload from the first content item of a tool result.
func resultText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	tc, ok := r.Content[0].(mcp.TextContent)
	if !ok {
		return ""
	}
	return tc.Text
}

// newTestServer builds a Server with the given CH service and a blank cache mock.
// Tests that exercise the cache should build the Server directly with their own mockFilingCache.
func newTestServer(svc CompanyHouseService) *Server {
	return New(svc, &mockFilingCache{})
}

func TestHandleSearchCompany(t *testing.T) {
	t.Parallel()

	t.Run("should return results for a valid query", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("SearchCompanies", mock.Anything, "Tesco", defaultSearchLimit).Return(
			[]companyhouse.SearchResult{
				{CompanyNumber: "00445790", Title: "TESCO PLC", CompanyStatus: "active", CompanyType: "plc"},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleSearchCompany, map[string]any{"query": "Tesco"})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
	})

	t.Run("should return a tool error when query is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleSearchCompany, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when company is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("SearchCompanies", mock.Anything, "NoSuchCompany", defaultSearchLimit).Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleSearchCompany, map[string]any{"query": "NoSuchCompany"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when unauthorized", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("SearchCompanies", mock.Anything, "Tesco", defaultSearchLimit).Return(nil, companyhouse.ErrUnauthorized)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleSearchCompany, map[string]any{"query": "Tesco"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when rate limited", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("SearchCompanies", mock.Anything, "Tesco", defaultSearchLimit).Return(nil, companyhouse.ErrRateLimited)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleSearchCompany, map[string]any{"query": "Tesco"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should propagate unexpected errors", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("SearchCompanies", mock.Anything, "Tesco", defaultSearchLimit).Return(nil, errors.New("network failure"))
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		_, err := callTool(srv.handleSearchCompany, map[string]any{"query": "Tesco"})

		// Assert
		assert.Error(t, err)
	})
}

func TestHandleGetCompanyProfile(t *testing.T) {
	t.Parallel()

	t.Run("should return the profile for a valid company number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetCompanyProfile", mock.Anything, "00445790").Return(
			&companyhouse.CompanyProfile{
				CompanyNumber: "00445790",
				CompanyName:   "TESCO PLC",
				CompanyStatus: "active",
				CompanyType:   "plc",
				SICCodes:      []string{"47110"},
				RegisteredOffice: companyhouse.RegisteredAddress{
					AddressLine1: "Tesco House",
					Locality:     "Welwyn Garden City",
					PostalCode:   "AL7 1GA",
				},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleGetCompanyProfile, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
	})

	t.Run("should return a tool error when company is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetCompanyProfile", mock.Anything, "99999999").Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleGetCompanyProfile, map[string]any{"ch_number": "99999999"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error for an invalid ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act — path traversal attempt
		result, err := callTool(srv.handleGetCompanyProfile, map[string]any{"ch_number": "../../etc/passwd"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when ch_number is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleGetCompanyProfile, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should propagate unexpected errors", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetCompanyProfile", mock.Anything, "00445790").Return(nil, errors.New("network failure"))
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		_, err := callTool(srv.handleGetCompanyProfile, map[string]any{"ch_number": "00445790"})

		// Assert
		assert.Error(t, err)
	})
}

func TestHandleListFilings(t *testing.T) {
	t.Parallel()

	const (
		testTransactionID = "MzI1MDk3NjkxOGFkaXF6a2N4"
		testDocURL        = "https://document-api.company-information.service.gov.uk/document/abc123"
		testDocumentID    = "11111111-2222-4333-8444-555555555555"
	)

	t.Run("should return filings for a valid company number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			ItemsPerPage: defaultFilingsLimit,
		}).Return(
			[]companyhouse.Filing{
				{
					TransactionID: testTransactionID,
					Type:          "AA",
					Description:   "full accounts made up to 25 February 2024",
					Date:          time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC),
					DocumentURL:   testDocURL,
				},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("StoreFilingRef", mock.Anything, "00445790", testTransactionID, testDocURL).Return(testDocumentID, nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleListFilings, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out []filingResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		require.Len(t, out, 1)
		assert.Equal(t, testDocumentID, out[0].DocumentID)
		assert.Equal(t, "AA", out[0].Type)
		assert.Equal(t, "2024-06-21", out[0].Date)
	})

	t.Run("should return a tool error when ch_number is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleListFilings, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error for an invalid ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleListFilings, map[string]any{"ch_number": "../../etc"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should propagate unexpected errors", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			ItemsPerPage: defaultFilingsLimit,
		}).Return(nil, errors.New("network failure"))
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		_, err := callTool(srv.handleListFilings, map[string]any{"ch_number": "00445790"})

		// Assert
		assert.Error(t, err)
	})
}

func TestHandleFetchFiling(t *testing.T) {
	t.Parallel()

	// docURL is the internal CH document URL stored in filing_refs.
	const docURL = "https://document-api.company-information.service.gov.uk/document/abc123"
	// testDocumentID is the opaque UUID that agents pass to fetch_filing.
	const testDocumentID = "11111111-2222-4333-8444-555555555555"

	t.Run("should return a cached document when already stored", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return(&cache.FilingEntry{LocalPath: "/cache/filing.pdf", ContentType: "application/pdf", FileSize: int64(100)}, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", "abc123").Return(([]cache.ZipEntryRecord)(nil), 0, nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, "cache", out.Source)
		assert.Equal(t, "/cache/filing.pdf", out.LocalPath)
		assert.Equal(t, testDocumentID, out.DocumentID)
		assert.False(t, out.IsArchive)
	})

	t.Run("should include is_archive and zip metadata on cache hit for a zip filing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return(&cache.FilingEntry{LocalPath: "/cache/report.xhtml", ContentType: "application/xhtml+xml", FileSize: int64(1000)}, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", "abc123").Return([]cache.ZipEntryRecord{
			{Filename: "report.xhtml", LocalPath: "/cache/report.xhtml", ContentType: "application/xhtml+xml", IsPrimary: true},
			{Filename: "report.pdf", LocalPath: "/cache/report.pdf", ContentType: "application/pdf", IsPrimary: false},
		}, 2, nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, "cache", out.Source)
		assert.True(t, out.IsArchive)
		assert.Equal(t, 2, out.TotalInArchive)
		assert.False(t, out.Truncated)
	})

	t.Run("should download and cache a document on cache miss", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(strings.NewReader("PDF content")),
				ContentType: "application/pdf",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("Put", mock.Anything, "00445790", "abc123", "application/pdf", "", mock.Anything).Return("/cache/filing.pdf", int64(11), nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, "companies_house", out.Source)
		assert.Equal(t, testDocumentID, out.DocumentID)
	})

	t.Run("should return a tool error when document is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when document_id is unknown", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "unknown-id").Return("", cache.ErrFilingRefNotFound)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": "unknown-id",
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
		assert.Contains(t, resultText(result), "list_filings")
	})

	t.Run("should return a tool error when ch_number is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{"document_id": testDocumentID})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when document_id is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error for an invalid ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "../../etc/passwd",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should extract primary xhtml from a zip response and set is_archive", func(t *testing.T) {
		t.Parallel()

		// Arrange
		xhtmlContent := "<html><body>iXBRL content</body></html>"
		zipBody := buildZip(t, [][2]string{
			{"dir/report-2024-T01.xhtml", xhtmlContent},
		})
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(bytes.NewReader(zipBody)),
				ContentType: "application/zip",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("PutZipEntries", mock.Anything, "00445790", "abc123",
			mock.MatchedBy(func(entries []cache.ZipCacheEntry) bool {
				return len(entries) == 1 &&
					entries[0].Filename == "report-2024-T01.xhtml" &&
					entries[0].ContentType == "application/xhtml+xml" &&
					entries[0].IsPrimary
			}),
			1,
		).Return("/cache/report-2024-T01.xhtml", nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, "application/xhtml+xml", out.ContentType)
		assert.Equal(t, "companies_house", out.Source)
		assert.True(t, out.IsArchive)
	})

	t.Run("should detect zip by magic bytes when Content-Type is wrong", func(t *testing.T) {
		t.Parallel()

		// Arrange — same zip payload but served with wrong Content-Type
		zipBody := buildZip(t, [][2]string{
			{"report.xhtml", "<html/>"},
		})
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(bytes.NewReader(zipBody)),
				ContentType: "application/octet-stream",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("PutZipEntries", mock.Anything, "00445790", "abc123",
			mock.MatchedBy(func(entries []cache.ZipCacheEntry) bool {
				return len(entries) == 1 &&
					entries[0].Filename == "report.xhtml" &&
					entries[0].ContentType == "application/xhtml+xml" &&
					entries[0].IsPrimary
			}),
			1,
		).Return("/cache/report.xhtml", nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, "application/xhtml+xml", out.ContentType)
		assert.True(t, out.IsArchive)
	})

	t.Run("should not set is_archive for non-zip responses", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(strings.NewReader("PDF content")),
				ContentType: "application/pdf",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("Put", mock.Anything, "00445790", "abc123", "application/pdf", "", mock.Anything).Return("/cache/filing.pdf", int64(11), nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.False(t, out.IsArchive)
	})

	t.Run("should set truncated true when archive exceeds MaxEntries cap", func(t *testing.T) {
		t.Parallel()

		// Arrange — build a zip with 21 files: one .xhtml (primary) + 20 .pdfs.
		// archive.ExtractAll caps at MaxEntries=20, so totalFiles=21 but len(entries)=20,
		// which should produce Truncated=true in the fetchResult.
		files := [][2]string{{"report.xhtml", "<xhtml/>"}}
		for i := range 20 {
			files = append(files, [2]string{fmt.Sprintf("page%02d.pdf", i), "PDF"})
		}
		zipBody := buildZip(t, files)
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(bytes.NewReader(zipBody)),
				ContentType: "application/zip",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("PutZipEntries", mock.Anything, "00445790", "abc123",
			mock.MatchedBy(func(entries []cache.ZipCacheEntry) bool {
				return len(entries) == archive.MaxEntries
			}),
			21,
		).Return("/cache/report.xhtml", nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.True(t, out.IsArchive)
		assert.True(t, out.Truncated)
		assert.Equal(t, 21, out.TotalInArchive)
	})

	t.Run("should return tool error when zip is malformed", func(t *testing.T) {
		t.Parallel()

		// Arrange — PK magic bytes but not a valid zip
		svc := &mockCHService{}
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(strings.NewReader("PK\x03\x04not a real zip")),
				ContentType: "application/zip",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleFetchFiling, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})
}

func TestHandleGetLatest(t *testing.T) {
	t.Parallel()

	const (
		docURL         = "https://document-api.company-information.service.gov.uk/document/abc123"
		testDocumentID = "11111111-2222-4333-8444-555555555555"
		transactionID  = "MzI1MDk3NjkxOGFkaXF6a2N4"
	)

	t.Run("should fetch and cache the latest filing for a category", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			Category:     "accounts",
			ItemsPerPage: 1,
		}).Return(
			[]companyhouse.Filing{
				{
					TransactionID: transactionID,
					Type:          "AA",
					Date:          time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC),
					DocumentURL:   docURL,
				},
			},
			nil,
		)
		svc.On("GetDocument", mock.Anything, docURL).Return(
			&companyhouse.Document{
				Body:        io.NopCloser(strings.NewReader("PDF content")),
				ContentType: "application/pdf",
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("StoreFilingRef", mock.Anything, "00445790", transactionID, docURL).Return(testDocumentID, nil)
		fc.On("Get", mock.Anything, "00445790", "abc123").Return((*cache.FilingEntry)(nil), nil)
		fc.On("Put", mock.Anything, "00445790", "abc123", "application/pdf", "", mock.Anything).Return("/cache/filing.pdf", int64(11), nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{
			"ch_number": "00445790",
			"category":  "accounts",
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out fetchResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.Equal(t, testDocumentID, out.DocumentID)
	})

	t.Run("should return a tool error when no filings exist for the category", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			Category:     "accounts",
			ItemsPerPage: 1,
		}).Return([]companyhouse.Filing{}, nil)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{
			"ch_number": "00445790",
			"category":  "accounts",
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when the latest filing has no downloadable document", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			Category:     "accounts",
			ItemsPerPage: 1,
		}).Return(
			[]companyhouse.Filing{
				{
					TransactionID: "MzI1MDk3NjkxOGFkaXF6a2N4",
					Type:          "AA",
					Date:          time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC),
					DocumentURL:   "", // no downloadable document
				},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		srv := newTestServer(svc)

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{
			"ch_number": "00445790",
			"category":  "accounts",
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when ch_number is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{"category": "accounts"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error when category is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error for an invalid ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleGetLatest, map[string]any{
			"ch_number": "../../etc",
			"category":  "accounts",
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})
}

func TestHandleClearCache(t *testing.T) {
	t.Parallel()

	t.Run("should delete all cached files and report freed space", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("Clear", mock.Anything, "").Return(cache.ClearResult{DeletedFiles: 2, FreedBytes: 500, DBRecords: 2}, nil)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleClearCache, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out clearCacheResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.EqualValues(t, 2, out.DeletedFiles)
		assert.EqualValues(t, 500, out.FreedBytes)
		assert.EqualValues(t, 2, out.DBRecordsRemoved)
	})

	t.Run("should delete only the specified company's cache", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("Clear", mock.Anything, "00445790").Return(cache.ClearResult{DeletedFiles: 1, FreedBytes: 250, DBRecords: 1}, nil)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleClearCache, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out clearCacheResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.EqualValues(t, 1, out.DeletedFiles)
	})

	t.Run("should succeed when the cache is empty", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("Clear", mock.Anything, "").Return(cache.ClearResult{}, nil)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleClearCache, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out clearCacheResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		assert.EqualValues(t, 0, out.DeletedFiles)
		assert.Equal(t, int64(0), out.FreedBytes)
	})

	t.Run("should propagate cache errors", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("Clear", mock.Anything, "").Return(cache.ClearResult{}, errors.New("disk error"))
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		_, err := callTool(srv.handleClearCache, map[string]any{})

		// Assert
		assert.Error(t, err)
	})

	t.Run("should return a tool error for an invalid ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := newTestServer(&mockCHService{})

		// Act
		result, err := callTool(srv.handleClearCache, map[string]any{"ch_number": "../../etc"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})
}

func TestHandleListFilingsFiltering(t *testing.T) {
	t.Parallel()

	t.Run("should omit filings that have no document URL", func(t *testing.T) {
		t.Parallel()

		// Arrange
		const (
			withDocURL = "https://document-api.company-information.service.gov.uk/document/abc123"
			withDocID  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
		)
		svc := &mockCHService{}
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			ItemsPerPage: defaultFilingsLimit,
		}).Return(
			[]companyhouse.Filing{
				{
					TransactionID: "with-doc",
					Type:          "AA",
					DocumentURL:   withDocURL,
				},
				{
					TransactionID: "no-doc",
					Type:          "CS01",
					DocumentURL:   "", // no downloadable document — StoreFilingRef must not be called
				},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		fc := &mockFilingCache{}
		fc.On("StoreFilingRef", mock.Anything, "00445790", "with-doc", withDocURL).Return(withDocID, nil)
		defer fc.AssertExpectations(t)
		srv := New(svc, fc)

		// Act
		result, err := callTool(srv.handleListFilings, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out []filingResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		require.Len(t, out, 1)
		assert.Equal(t, withDocID, out[0].DocumentID)
	})
}

func TestHandleExtractXBRLFacts(t *testing.T) {
	t.Parallel()

	// writeXHTMLInCache writes a minimal iXBRL file into a fake cache directory
	// and returns the cache dir and the file path.
	writeXHTMLInCache := func(t *testing.T, content string) (cacheDir, filePath string) {
		t.Helper()
		cacheDir = t.TempDir()
		dir := filepath.Join(cacheDir, "cache", "uk", "12345678", "doc-001")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		filePath = filepath.Join(dir, "filing.xhtml")
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0o600))
		return cacheDir, filePath
	}

	const minimalXHTML = `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`

	t.Run("should return structured facts for a valid cached file", func(t *testing.T) {
		t.Parallel()

		// Arrange
		_, filePath := writeXHTMLInCache(t, minimalXHTML)
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", filePath).Return(filePath, nil)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": filePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError, "unexpected tool error: %v", result.Content)
		var out struct {
			Facts     []map[string]any `json:"facts"`
			Count     int              `json:"count"`
			Truncated bool             `json:"truncated"`
		}
		require.NotEmpty(t, result.Content)
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &out))
		require.Len(t, out.Facts, 1)
		assert.Equal(t, "Revenue", out.Facts[0]["name"])
		assert.Equal(t, 1, out.Count)
		assert.False(t, out.Truncated)
	})

	t.Run("should return error when path is outside cache directory", func(t *testing.T) {
		t.Parallel()

		// Arrange — ValidatePath signals the path is outside the cache subtree.
		outsideDir := t.TempDir()
		outsidePath := filepath.Join(outsideDir, "secret.xhtml")
		require.NoError(t, os.WriteFile(outsidePath, []byte(minimalXHTML), 0o600))
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", outsidePath).Return("", cache.ErrOutsideCache)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": outsidePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "not within the cache directory")
	})

	t.Run("should return error when path resolves via symlink to outside cache directory", func(t *testing.T) {
		t.Parallel()

		// Arrange — ValidatePath sees the resolved symlink target is outside the cache.
		linkPath := filepath.Join(t.TempDir(), "filing.xhtml")
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", linkPath).Return("", cache.ErrOutsideCache)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": linkPath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "not within the cache directory")
	})

	t.Run("should return error for non-.xhtml extension", func(t *testing.T) {
		t.Parallel()

		// Arrange
		filingCache := &mockFilingCache{}
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": "/some/path/report.pdf"})

		// Assert
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].(mcp.TextContent).Text, ".xhtml or .html")
	})

	t.Run("should return error when local_path is missing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		filingCache := &mockFilingCache{}
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{})

		// Assert
		require.NoError(t, err)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].(mcp.TextContent).Text, "local_path is required")
	})

	t.Run("should include render_type native_ixbrl for a plain iXBRL document", func(t *testing.T) {
		t.Parallel()

		// Arrange — standard iXBRL with normal prose: no pdf2htmlEX markers.
		nativeXHTML := `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<p>The company delivered strong results in the financial year ended December 2024.</p>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`
		_, filePath := writeXHTMLInCache(t, nativeXHTML)
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", filePath).Return(filePath, nil)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": filePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		var out struct {
			RenderType string   `json:"render_type"`
			Warnings   []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &out))
		assert.Equal(t, "native_ixbrl", out.RenderType)
		assert.Empty(t, out.Warnings)
	})

	t.Run("should include render_type pdf_rendered and generic warning when not from a zip", func(t *testing.T) {
		t.Parallel()

		// Arrange — document contains the canonical pdf2htmlEX page wrapper <div class="pf">.
		pdfXHTML := `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<div class="pf"><div class="pc"><span class="t">A</span></div></div>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`
		_, filePath := writeXHTMLInCache(t, pdfXHTML)
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", filePath).Return(filePath, nil)
		filingCache.On("ParseFilingPath", filePath).Return("12345678", "doc-001", nil)
		filingCache.On("GetZipEntries", mock.Anything, "12345678", "doc-001").Return(([]cache.ZipEntryRecord)(nil), 0, nil)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": filePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		var out struct {
			RenderType string   `json:"render_type"`
			Warnings   []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &out))
		assert.Equal(t, "pdf_rendered", out.RenderType)
		require.Len(t, out.Warnings, 1)
		assert.Contains(t, out.Warnings[0], "narrative text")
		assert.Contains(t, out.Warnings[0], "consider fetching an alternative filing format")
	})

	t.Run("should name available alternatives in warning when pdf_rendered and zip has non-primary entries", func(t *testing.T) {
		t.Parallel()

		// Arrange — pdf2htmlEX document from a zip that also contains a PDF alternative.
		pdfXHTML := `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<div class="pf"><div class="pc"><span class="t">A</span></div></div>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`
		_, filePath := writeXHTMLInCache(t, pdfXHTML)
		records := []cache.ZipEntryRecord{
			{Filename: "report.xhtml", LocalPath: filePath, ContentType: "application/xhtml+xml", FileSize: 1000, IsPrimary: true},
			{Filename: "report.pdf", LocalPath: "/cache/report.pdf", ContentType: "application/pdf", FileSize: 500000, IsPrimary: false},
		}
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", filePath).Return(filePath, nil)
		filingCache.On("ParseFilingPath", filePath).Return("12345678", "doc-001", nil)
		filingCache.On("GetZipEntries", mock.Anything, "12345678", "doc-001").Return(records, len(records), nil)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": filePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		var out struct {
			RenderType string   `json:"render_type"`
			Warnings   []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &out))
		assert.Equal(t, "pdf_rendered", out.RenderType)
		require.Len(t, out.Warnings, 1)
		assert.Contains(t, out.Warnings[0], "narrative text")
		assert.Contains(t, out.Warnings[0], "report.pdf")
		assert.Contains(t, out.Warnings[0], "list_zip_contents")
		assert.Contains(t, out.Warnings[0], "12345678")
		assert.Contains(t, out.Warnings[0], "document_id")
	})

	t.Run("should say no alternatives available when pdf_rendered and all zip entries are primary", func(t *testing.T) {
		t.Parallel()

		// Arrange — zip with only the primary iXBRL document (no PDF companion).
		pdfXHTML := `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<div class="pf"><div class="pc"><span class="t">A</span></div></div>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`
		_, filePath := writeXHTMLInCache(t, pdfXHTML)
		records := []cache.ZipEntryRecord{
			{Filename: "report.xhtml", LocalPath: filePath, ContentType: "application/xhtml+xml", FileSize: 1000, IsPrimary: true},
		}
		filingCache := &mockFilingCache{}
		defer filingCache.AssertExpectations(t)
		filingCache.On("ValidatePath", filePath).Return(filePath, nil)
		filingCache.On("ParseFilingPath", filePath).Return("12345678", "doc-001", nil)
		filingCache.On("GetZipEntries", mock.Anything, "12345678", "doc-001").Return(records, len(records), nil)
		srv := New(&mockCHService{}, filingCache)

		// Act
		result, err := callTool(srv.handleExtractXBRLFacts, map[string]any{"local_path": filePath})

		// Assert
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		var out struct {
			RenderType string   `json:"render_type"`
			Warnings   []string `json:"warnings"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &out))
		assert.Equal(t, "pdf_rendered", out.RenderType)
		require.Len(t, out.Warnings, 1)
		assert.Contains(t, out.Warnings[0], "no alternative formats are available")
	})
}

func TestHandleListZipContents(t *testing.T) {
	t.Parallel()

	const (
		docURL         = "https://document-api.company-information.service.gov.uk/document/abc123"
		testDocumentID = "11111111-2222-4333-8444-555555555555"
	)

	t.Run("should return manifest for a cached zip filing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		records := []cache.ZipEntryRecord{
			{Filename: "report.xhtml", LocalPath: "/cache/report.xhtml", ContentType: "application/xhtml+xml", FileSize: 12345, IsPrimary: true},
			{Filename: "report.pdf", LocalPath: "/cache/report.pdf", ContentType: "application/pdf", FileSize: 4823091, IsPrimary: false},
		}
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", "abc123").Return(records, len(records), nil)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleListZipContents, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.False(t, isToolError(result))
		var out listZipContentsResult
		require.NoError(t, json.Unmarshal([]byte(resultText(result)), &out))
		require.Len(t, out.Entries, 2)
		assert.True(t, out.Entries[0].IsPrimary)
		assert.Equal(t, "report.xhtml", out.Entries[0].Filename)
		assert.Equal(t, "/cache/report.xhtml", out.Entries[0].LocalPath)
		assert.Equal(t, "application/xhtml+xml", out.Entries[0].ContentType)
		assert.EqualValues(t, 12345, out.Entries[0].FileSizeBytes)
		assert.False(t, out.Entries[1].IsPrimary)
		assert.Equal(t, "report.pdf", out.Entries[1].Filename)
		assert.Equal(t, 2, out.TotalInArchive)
		assert.False(t, out.Truncated)
	})

	t.Run("should return a tool error when filing is not cached or not a zip", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", "abc123").Return(([]cache.ZipEntryRecord)(nil), 0, nil)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleListZipContents, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
		assert.Contains(t, resultText(result), "not cached")
	})

	t.Run("should return a tool error when document_id is unknown", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "unknown-id").Return("", cache.ErrFilingRefNotFound)
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		result, err := callTool(srv.handleListZipContents, map[string]any{
			"ch_number":   "00445790",
			"document_id": "unknown-id",
		})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
		assert.Contains(t, resultText(result), "list_filings")
	})

	t.Run("should return a tool error for missing ch_number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := New(&mockCHService{}, &mockFilingCache{})

		// Act
		result, err := callTool(srv.handleListZipContents, map[string]any{"document_id": testDocumentID})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should return a tool error for missing document_id", func(t *testing.T) {
		t.Parallel()

		// Arrange
		srv := New(&mockCHService{}, &mockFilingCache{})

		// Act
		result, err := callTool(srv.handleListZipContents, map[string]any{"ch_number": "00445790"})

		// Assert
		require.NoError(t, err)
		assert.True(t, isToolError(result))
	})

	t.Run("should propagate cache errors", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", testDocumentID).Return(docURL, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", "abc123").Return(([]cache.ZipEntryRecord)(nil), 0, errors.New("db error"))
		defer fc.AssertExpectations(t)
		srv := New(&mockCHService{}, fc)

		// Act
		_, err := callTool(srv.handleListZipContents, map[string]any{
			"ch_number":   "00445790",
			"document_id": testDocumentID,
		})

		// Assert
		assert.Error(t, err)
	})
}
