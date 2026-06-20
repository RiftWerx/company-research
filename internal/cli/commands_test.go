package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
	"github.com/riftwerx/company-research/internal/result"
)

// mockCHSvc is a testify mock for CompanyHouseService.
type mockCHSvc struct {
	mock.Mock
}

func (m *mockCHSvc) SearchCompanies(ctx context.Context, query string, limit int) ([]companyhouse.SearchResult, error) {
	args := m.Called(ctx, query, limit)
	results, _ := args.Get(0).([]companyhouse.SearchResult)
	return results, args.Error(1)
}

func (m *mockCHSvc) GetCompanyProfile(ctx context.Context, number string) (*companyhouse.CompanyProfile, error) {
	args := m.Called(ctx, number)
	profile, _ := args.Get(0).(*companyhouse.CompanyProfile)
	return profile, args.Error(1)
}

func (m *mockCHSvc) GetFilingHistory(ctx context.Context, number string, opts companyhouse.ListFilingsOptions) ([]companyhouse.Filing, error) {
	args := m.Called(ctx, number, opts)
	filings, _ := args.Get(0).([]companyhouse.Filing)
	return filings, args.Error(1)
}

func (m *mockCHSvc) GetDocument(ctx context.Context, url string) (*companyhouse.Document, error) {
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

func (m *mockFilingCache) Clear(ctx context.Context, chNumber string) (cache.ClearResult, error) {
	args := m.Called(ctx, chNumber)
	r, _ := args.Get(0).(cache.ClearResult)
	return r, args.Error(1)
}

func (m *mockFilingCache) ValidatePath(path string) (string, error) {
	args := m.Called(path)
	return args.String(0), args.Error(1)
}

// chDocURL is a valid CH document API URL usable in tests.
const chDocURL = "https://document-api.company-information.service.gov.uk/document/abcDEF123456/content"

// chDocID is the document ID extracted from chDocURL by ParseDocumentID.
const chDocID = "abcDEF123456"

func TestSearchCompanyCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON results for a valid query", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		svc.On("SearchCompanies", mock.Anything, "Tesco", 10).Return(
			[]companyhouse.SearchResult{
				{CompanyNumber: "00445790", Title: "TESCO PLC", CompanyStatus: "active", CompanyType: "plc"},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &SearchCompanyCmd{Query: "Tesco", Limit: 10, out: &buf}

		// Act
		err := cmd.Run(context.Background(), svc)

		// Assert
		require.NoError(t, err)
		var got []result.SearchResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		require.Len(t, got, 1)
		assert.Equal(t, "00445790", got[0].CHNumber)
		assert.Equal(t, "TESCO PLC", got[0].Name)
	})

	t.Run("should return error when company is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		svc.On("SearchCompanies", mock.Anything, "XYZNOTREAL", 10).Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		cmd := &SearchCompanyCmd{Query: "XYZNOTREAL", Limit: 10, out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), svc)

		// Assert
		require.Error(t, err)
	})
}

func TestGetCompanyProfileCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON profile for a valid CH number", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		svc.On("GetCompanyProfile", mock.Anything, "00445790").Return(
			&companyhouse.CompanyProfile{
				CompanyNumber: "00445790",
				CompanyName:   "TESCO PLC",
				CompanyStatus: "active",
				CompanyType:   "plc",
				SICCodes:      []string{"47110"},
			},
			nil,
		)
		defer svc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &GetCompanyProfileCmd{CHNumber: "00445790", out: &buf}

		// Act
		err := cmd.Run(context.Background(), svc)

		// Assert
		require.NoError(t, err)
		var got result.ProfileResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, "00445790", got.CHNumber)
		assert.Equal(t, "TESCO PLC", got.Name)
	})

	t.Run("should return error for an invalid CH number", func(t *testing.T) {
		t.Parallel()

		// Arrange — no service call expected
		cmd := &GetCompanyProfileCmd{CHNumber: "invalid!", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), &mockCHSvc{})

		// Assert
		require.Error(t, err)
	})
}

func TestListFilingsCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON filings for a valid company", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		fc := &mockFilingCache{}
		filingDate := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			Category:     "accounts",
			StartIndex:   0,
			ItemsPerPage: 20,
		}).Return(
			[]companyhouse.Filing{
				{TransactionID: "txn-1", Type: "AA", Description: "Annual accounts", Date: filingDate, DocumentURL: chDocURL},
			},
			nil,
		)
		fc.On("StoreFilingRef", mock.Anything, "00445790", "txn-1", chDocURL).Return("uuid-001", nil)
		defer svc.AssertExpectations(t)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &ListFilingsCmd{CHNumber: "00445790", Category: "accounts", Start: 0, Limit: 20, out: &buf}

		// Act
		err := cmd.Run(context.Background(), svc, fc)

		// Assert
		require.NoError(t, err)
		var got []result.FilingResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		require.Len(t, got, 1)
		assert.Equal(t, "uuid-001", got[0].DocumentID)
		assert.Equal(t, "2024-03-01", got[0].Date)
	})

	t.Run("should return error when company is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		fc := &mockFilingCache{}
		svc.On("GetFilingHistory", mock.Anything, "00000001", mock.Anything).Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		cmd := &ListFilingsCmd{CHNumber: "00000001", Limit: 20, out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), svc, fc)

		// Assert
		require.Error(t, err)
	})
}

func TestFetchFilingCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON fetch result for a valid document ID", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		fc := &mockFilingCache{}
		body := io.NopCloser(strings.NewReader("filing content"))
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "uuid-001").Return(chDocURL, nil)
		fc.On("Get", mock.Anything, "00445790", chDocID).Return(nil, nil) // cache miss
		svc.On("GetDocument", mock.Anything, chDocURL).Return(
			&companyhouse.Document{Body: body, ContentType: "application/xhtml+xml"}, nil)
		fc.On("Put", mock.Anything, "00445790", chDocID, "application/xhtml+xml", "", mock.Anything).
			Return("/cache/00445790/"+chDocID+"/filing.xhtml", int64(14), nil)
		defer svc.AssertExpectations(t)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &FetchFilingCmd{CHNumber: "00445790", DocumentID: "uuid-001", out: &buf}

		// Act
		err := cmd.Run(context.Background(), svc, fc)

		// Assert
		require.NoError(t, err)
		var got result.FetchResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, "uuid-001", got.DocumentID)
		assert.Equal(t, "companies_house", got.Source)
		assert.Equal(t, int64(14), got.FileSizeBytes)
	})

	t.Run("should return error when document ID is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "bad-uuid").Return("", cache.ErrFilingRefNotFound)
		defer fc.AssertExpectations(t)
		cmd := &FetchFilingCmd{CHNumber: "00445790", DocumentID: "bad-uuid", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), &mockCHSvc{}, fc)

		// Assert
		require.Error(t, err)
	})
}

func TestGetLatestCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON fetch result for the latest filing in a category", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		fc := &mockFilingCache{}
		body := io.NopCloser(strings.NewReader("latest filing"))
		svc.On("GetFilingHistory", mock.Anything, "00445790", companyhouse.ListFilingsOptions{
			Category:     "accounts",
			ItemsPerPage: 1,
		}).Return(
			[]companyhouse.Filing{
				{TransactionID: "txn-latest", Type: "AA", DocumentURL: chDocURL},
			},
			nil,
		)
		fc.On("StoreFilingRef", mock.Anything, "00445790", "txn-latest", chDocURL).Return("uuid-latest", nil)
		fc.On("Get", mock.Anything, "00445790", chDocID).Return(nil, nil) // cache miss
		svc.On("GetDocument", mock.Anything, chDocURL).Return(
			&companyhouse.Document{Body: body, ContentType: "application/xhtml+xml"}, nil)
		fc.On("Put", mock.Anything, "00445790", chDocID, "application/xhtml+xml", "", mock.Anything).
			Return("/cache/00445790/"+chDocID+"/filing.xhtml", int64(13), nil)
		defer svc.AssertExpectations(t)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &GetLatestCmd{CHNumber: "00445790", Category: "accounts", out: &buf}

		// Act
		err := cmd.Run(context.Background(), svc, fc)

		// Assert
		require.NoError(t, err)
		var got result.FetchResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, "uuid-latest", got.DocumentID)
		assert.Equal(t, "companies_house", got.Source)
	})

	t.Run("should return error when no filings are found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		svc := &mockCHSvc{}
		svc.On("GetFilingHistory", mock.Anything, "00000001", mock.Anything).Return(nil, companyhouse.ErrNotFound)
		defer svc.AssertExpectations(t)
		cmd := &GetLatestCmd{CHNumber: "00000001", Category: "accounts", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), svc, &mockFilingCache{})

		// Assert
		require.Error(t, err)
	})
}

func TestListZipContentsCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON zip entries for a cached archive", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "uuid-001").Return(chDocURL, nil)
		fc.On("GetZipEntries", mock.Anything, "00445790", chDocID).Return(
			[]cache.ZipEntryRecord{
				{Filename: "accounts.xhtml", LocalPath: "/cache/accounts.xhtml", ContentType: "application/xhtml+xml", FileSize: 500, IsPrimary: true},
			},
			1, nil,
		)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &ListZipContentsCmd{CHNumber: "00445790", DocumentID: "uuid-001", out: &buf}

		// Act
		err := cmd.Run(context.Background(), fc)

		// Assert
		require.NoError(t, err)
		var got result.ListZipContentsResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		require.Len(t, got.Entries, 1)
		assert.Equal(t, "accounts.xhtml", got.Entries[0].Filename)
		assert.True(t, got.Entries[0].IsPrimary)
	})

	t.Run("should return error when document ID is not found", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("ResolveFilingRef", mock.Anything, "00445790", "bad-uuid").Return("", cache.ErrFilingRefNotFound)
		defer fc.AssertExpectations(t)
		cmd := &ListZipContentsCmd{CHNumber: "00445790", DocumentID: "bad-uuid", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), fc)

		// Assert
		require.Error(t, err)
	})
}

func TestExtractXBRLFactsCmd(t *testing.T) {
	t.Parallel()

	const minimalXHTML = `<!DOCTYPE html><html><body>
<xbrli:context id="c1">
  <xbrli:entity><xbrli:identifier scheme="x">1</xbrli:identifier></xbrli:entity>
  <xbrli:period><xbrli:instant>2024-12-31</xbrli:instant></xbrli:period>
</xbrli:context>
<xbrli:unit id="GBP"><xbrli:measure>iso4217:GBP</xbrli:measure></xbrli:unit>
<ix:nonFraction name="frs102:Revenue" contextRef="c1" unitRef="GBP" decimals="0">100</ix:nonFraction>
</body></html>`

	t.Run("should return JSON facts for a valid cached iXBRL file", func(t *testing.T) {
		t.Parallel()

		// Arrange — write a real file so xbrl.ParseFacts can read it.
		dir := filepath.Join(t.TempDir(), "cache", "uk", "00445790", "doc-001")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		filePath := filepath.Join(dir, "filing.xhtml")
		require.NoError(t, os.WriteFile(filePath, []byte(minimalXHTML), 0o600))
		fc := &mockFilingCache{}
		fc.On("ValidatePath", filePath).Return(filePath, nil)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &ExtractXBRLFactsCmd{LocalPath: filePath, out: &buf}

		// Act
		err := cmd.Run(context.Background(), fc)

		// Assert
		require.NoError(t, err)
		var got result.XBRLFactsResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, 1, got.Count)
		assert.False(t, got.Truncated)
	})

	t.Run("should return error for a non-xhtml file extension", func(t *testing.T) {
		t.Parallel()

		// Arrange — extension check fires before any cache or FS call
		cmd := &ExtractXBRLFactsCmd{LocalPath: "/some/path/report.pdf", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), &mockFilingCache{})

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), ".xhtml or .html")
	})
}

func TestClearCacheCmd(t *testing.T) {
	t.Parallel()

	t.Run("should return JSON clear result for a successful clear", func(t *testing.T) {
		t.Parallel()

		// Arrange
		fc := &mockFilingCache{}
		fc.On("Clear", mock.Anything, "").Return(
			cache.ClearResult{DeletedFiles: 5, FreedBytes: 1024, DBRecords: 3}, nil)
		defer fc.AssertExpectations(t)
		var buf bytes.Buffer
		cmd := &ClearCacheCmd{out: &buf}

		// Act
		err := cmd.Run(context.Background(), fc)

		// Assert
		require.NoError(t, err)
		var got result.ClearCacheResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		assert.Equal(t, int64(5), got.DeletedFiles)
		assert.Equal(t, int64(1024), got.FreedBytes)
		assert.Equal(t, int64(3), got.DBRecordsRemoved)
	})

	t.Run("should return error for an invalid CH number", func(t *testing.T) {
		t.Parallel()

		// Arrange — CH number validation fires before any cache call
		cmd := &ClearCacheCmd{CHNumber: "invalid!", out: io.Discard}

		// Act
		err := cmd.Run(context.Background(), &mockFilingCache{})

		// Assert
		require.Error(t, err)
	})
}
