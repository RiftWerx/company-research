package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/riftwerx/company-research/internal/archive"
	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
	"github.com/riftwerx/company-research/internal/xbrl"
)

// CompanyHouseService is the subset of companyhouse.Service that MCP handlers require.
type CompanyHouseService interface {
	SearchCompanies(ctx context.Context, query string, maxResults int) ([]companyhouse.SearchResult, error)
	GetCompanyProfile(ctx context.Context, chNumber string) (*companyhouse.CompanyProfile, error)
	GetFilingHistory(ctx context.Context, chNumber string, opts companyhouse.ListFilingsOptions) ([]companyhouse.Filing, error)
	GetDocument(ctx context.Context, documentURL string) (*companyhouse.Document, error)
}

// FilingCache is the subset of cache.Cache that MCP handlers require.
type FilingCache interface {
	Get(ctx context.Context, chNumber, docID string) (*cache.FilingEntry, error)
	Put(ctx context.Context, chNumber, docID, contentType, filename string, body io.Reader) (localPath string, written int64, err error)
	PutZipEntries(ctx context.Context, chNumber, docID string, entries []cache.ZipCacheEntry, totalInArchive int) (primaryPath string, err error)
	GetZipEntries(ctx context.Context, chNumber, docID string) ([]cache.ZipEntryRecord, int, error)
	ParseFilingPath(realPath string) (chNumber, docID string, err error)
	StoreFilingRef(ctx context.Context, chNumber, transactionID, documentURL string) (documentID string, err error)
	ResolveFilingRef(ctx context.Context, chNumber, documentID string) (documentURL string, err error)
	Clear(ctx context.Context, chNumber string) (cache.ClearResult, error)
	ValidatePath(path string) (string, error)
}

// defaultSearchLimit is the maximum number of search results returned when the caller does not specify a limit.
const defaultSearchLimit = 10

// defaultFilingsLimit is the maximum number of filings returned when the caller does not specify a limit.
const defaultFilingsLimit = 20

// searchResult is the minimal per-company response for search_company.
type searchResult struct {
	CHNumber string `json:"ch_number"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Type     string `json:"type"`
	Locality string `json:"locality,omitempty"`
}

// profileAddress is the address sub-object in get_company_profile responses.
type profileAddress struct {
	Line1    string `json:"line1,omitempty"`
	Line2    string `json:"line2,omitempty"`
	Locality string `json:"locality,omitempty"`
	Postcode string `json:"postcode,omitempty"`
	Country  string `json:"country,omitempty"`
}

// profileResult is the minimal response for get_company_profile.
type profileResult struct {
	CHNumber       string         `json:"ch_number"`
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	Type           string         `json:"type"`
	DateOfCreation string         `json:"date_of_creation,omitempty"`
	SICCodes       []string       `json:"sic_codes"`
	Address        profileAddress `json:"address"`
}

// filingResult is the minimal per-filing response for list_filings.
type filingResult struct {
	DocumentID  string `json:"document_id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Date        string `json:"date"` // YYYY-MM-DD
}

// fetchResult is the response for fetch_filing and get_latest.
type fetchResult struct {
	DocumentID     string `json:"document_id"`
	LocalPath      string `json:"local_path"`
	ContentType    string `json:"content_type"`
	FileSizeBytes  int64  `json:"file_size_bytes"`
	Source         string `json:"source"`
	IsArchive      bool   `json:"is_archive,omitempty"`
	TotalInArchive int    `json:"total_in_archive,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}

// clearCacheResult is the response for clear_cache.
type clearCacheResult struct {
	DeletedFiles     int64 `json:"deleted_files"`
	FreedBytes       int64 `json:"freed_bytes"`
	DBRecordsRemoved int64 `json:"db_records_removed"`
}

// handleSearchCompany implements the search_company tool.
func (s *Server) handleSearchCompany(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return toolError("query is required")
	}
	limit := req.GetInt("limit", defaultSearchLimit)

	results, err := s.chSvc.SearchCompanies(ctx, query, limit)
	if err != nil {
		return toolResultForCHError(err, "search companies")
	}

	out := make([]searchResult, len(results))
	for i, r := range results {
		out[i] = searchResult{
			CHNumber: r.CompanyNumber,
			Name:     r.Title,
			Status:   r.CompanyStatus,
			Type:     r.CompanyType,
			Locality: r.Address.Locality,
		}
	}
	return toolResultJSON(out)
}

// handleGetCompanyProfile implements the get_company_profile tool.
func (s *Server) handleGetCompanyProfile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber, err := req.RequireString("ch_number")
	if err != nil {
		return toolError("ch_number is required")
	}
	if !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}

	profile, err := s.chSvc.GetCompanyProfile(ctx, chNumber)
	if err != nil {
		return toolResultForCHError(err, "get company profile")
	}

	sicCodes := profile.SICCodes
	if sicCodes == nil {
		sicCodes = []string{}
	}
	out := profileResult{
		CHNumber:       profile.CompanyNumber,
		Name:           profile.CompanyName,
		Status:         profile.CompanyStatus,
		Type:           profile.CompanyType,
		DateOfCreation: profile.DateOfCreation,
		SICCodes:       sicCodes,
		Address: profileAddress{
			Line1:    profile.RegisteredOffice.AddressLine1,
			Line2:    profile.RegisteredOffice.AddressLine2,
			Locality: profile.RegisteredOffice.Locality,
			Postcode: profile.RegisteredOffice.PostalCode,
			Country:  profile.RegisteredOffice.Country,
		},
	}
	return toolResultJSON(out)
}

// handleListFilings implements the list_filings tool.
func (s *Server) handleListFilings(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber, err := req.RequireString("ch_number")
	if err != nil {
		return toolError("ch_number is required")
	}
	if !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}
	category := req.GetString("category", "")
	start := req.GetInt("start", 0)
	limit := req.GetInt("limit", defaultFilingsLimit)

	filings, err := s.chSvc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     category,
		StartIndex:   start,
		ItemsPerPage: limit,
	})
	if err != nil {
		return toolResultForCHError(err, "list filings")
	}

	// Omit filings that have no downloadable document — they cannot be used with
	// fetch_filing and would only produce confusing errors if the LLM tried.
	out := make([]filingResult, 0, len(filings))
	for _, f := range filings {
		if f.DocumentURL == "" {
			continue
		}
		docID, refErr := s.cache.StoreFilingRef(ctx, chNumber, f.TransactionID, f.DocumentURL)
		if refErr != nil {
			return nil, fmt.Errorf("store filing ref: %w", refErr)
		}
		date := ""
		if !f.Date.IsZero() {
			date = f.Date.Format("2006-01-02")
		}
		out = append(out, filingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        date,
		})
	}
	return toolResultJSON(out)
}

// handleFetchFiling implements the fetch_filing tool.
func (s *Server) handleFetchFiling(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber, err := req.RequireString("ch_number")
	if err != nil {
		return toolError("ch_number is required")
	}
	if !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}
	documentID, err := req.RequireString("document_id")
	if err != nil {
		return toolError("document_id is required")
	}
	documentURL, refErr := s.cache.ResolveFilingRef(ctx, chNumber, documentID)
	if errors.Is(refErr, cache.ErrFilingRefNotFound) {
		return toolError("document_id not found; call list_filings or get_latest first to obtain a valid document_id")
	}
	if refErr != nil {
		return nil, fmt.Errorf("resolve filing ref: %w", refErr)
	}
	return s.fetchDocument(ctx, chNumber, documentURL, documentID)
}

// handleGetLatest implements the get_latest tool.
func (s *Server) handleGetLatest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber, err := req.RequireString("ch_number")
	if err != nil {
		return toolError("ch_number is required")
	}
	if !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}
	category, err := req.RequireString("category")
	if err != nil {
		return toolError("category is required")
	}

	filings, err := s.chSvc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     category,
		ItemsPerPage: 1,
	})
	if err != nil {
		return toolResultForCHError(err, "list filings")
	}
	if len(filings) == 0 {
		return toolError("no filings found for that category")
	}
	if filings[0].DocumentURL == "" {
		return toolError("most recent filing in that category has no downloadable document")
	}

	documentID, refErr := s.cache.StoreFilingRef(ctx, chNumber, filings[0].TransactionID, filings[0].DocumentURL)
	if refErr != nil {
		return nil, fmt.Errorf("store filing ref: %w", refErr)
	}
	return s.fetchDocument(ctx, chNumber, filings[0].DocumentURL, documentID)
}

// fetchDocument retrieves a filing from the cache or downloads it from CH.
// documentURL is a trusted CH document API URL resolved from the filing_refs table.
// documentID is the opaque UUID to include in the response.
// Returns a cached result immediately if available; otherwise fetches from CH and stores it.
func (s *Server) fetchDocument(ctx context.Context, chNumber, documentURL, documentID string) (*mcp.CallToolResult, error) {
	// The URL arrives from our own DB (filing_refs), so these are internal validations,
	// not user-input checks. They guard against corrupt DB values.
	if !companyhouse.ValidateDocumentURL(documentURL) {
		return nil, fmt.Errorf("stored document_url is not a valid CH document API URL: %s", documentURL)
	}

	docID, ok := companyhouse.ParseDocumentID(documentURL)
	if !ok {
		return nil, fmt.Errorf("stored document_url does not contain a recognisable CH document ID: %s", documentURL)
	}
	if !companyhouse.ValidateDocID(docID) {
		return nil, fmt.Errorf("stored document_url contains an invalid document ID: %s", documentURL)
	}

	entry, err := s.cache.Get(ctx, chNumber, docID)
	if err != nil {
		return nil, fmt.Errorf("check cache: %w", err)
	}
	if entry != nil {
		res := fetchResult{
			DocumentID:    documentID,
			LocalPath:     entry.LocalPath,
			ContentType:   entry.ContentType,
			FileSizeBytes: entry.FileSize,
			Source:        "cache",
		}
		zipRecords, totalInArchive, zipErr := s.cache.GetZipEntries(ctx, chNumber, docID)
		if zipErr != nil {
			return nil, fmt.Errorf("check zip entries: %w", zipErr)
		}
		if len(zipRecords) > 0 {
			res.IsArchive = true
			res.TotalInArchive = totalInArchive
			res.Truncated = totalInArchive > len(zipRecords)
		}
		return toolResultJSON(res)
	}

	doc, err := s.chSvc.GetDocument(ctx, documentURL)
	if err != nil {
		return toolResultForCHError(err, "fetch document")
	}
	defer doc.Body.Close() // captures original body; safe to replace doc.Body below

	// Detect zip by Content-Type first; fall back to magic bytes PK\x03\x04.
	peek := make([]byte, 4)
	n, _ := io.ReadFull(doc.Body, peek)
	peeked := peek[:n]
	isZip := doc.ContentType == "application/zip" ||
		(n >= 4 && peeked[0] == 'P' && peeked[1] == 'K' && peeked[2] == 0x03 && peeked[3] == 0x04)
	// Reconstruct the body so the peeked bytes are not consumed.
	doc.Body = io.NopCloser(io.MultiReader(bytes.NewReader(peeked), doc.Body))

	if isZip {
		zipData, readErr := archive.ReadBody(doc.Body, cache.MaxFileSizeBytes)
		if errors.Is(readErr, archive.ErrBodyTooLarge) {
			// Too-large is a user-facing condition; other read errors are unexpected and propagate.
			return toolError(fmt.Sprintf("zip filing exceeds %d-byte size limit", cache.MaxFileSizeBytes))
		}
		if readErr != nil {
			return nil, fmt.Errorf("read zip: %w", readErr)
		}
		zipEntries, totalInArchive, extractErr := archive.ExtractAll(zipData, cache.MaxFileSizeBytes)
		if extractErr != nil {
			return toolError(fmt.Sprintf("unpack zip: %s", extractErr))
		}
		cacheEntries := make([]cache.ZipCacheEntry, len(zipEntries))
		for i, e := range zipEntries {
			cacheEntries[i] = cache.ZipCacheEntry{
				Filename:    e.Filename,
				ContentType: e.ContentType,
				Content:     e.Content,
				IsPrimary:   e.IsPrimary,
			}
		}
		primaryPath, cacheErr := s.cache.PutZipEntries(ctx, chNumber, docID, cacheEntries, totalInArchive)
		if cacheErr != nil {
			return nil, fmt.Errorf("cache zip entries: %w", cacheErr)
		}
		primary := zipEntries[0] // ExtractAll guarantees primary is first
		return toolResultJSON(fetchResult{
			DocumentID:     documentID,
			LocalPath:      primaryPath,
			ContentType:    primary.ContentType,
			FileSizeBytes:  int64(len(primary.Content)),
			Source:         "companies_house",
			IsArchive:      true,
			TotalInArchive: totalInArchive,
			Truncated:      totalInArchive > len(zipEntries),
		})
	}

	localPath, written, err := s.cache.Put(ctx, chNumber, docID, doc.ContentType, "", doc.Body)
	if err != nil {
		return nil, fmt.Errorf("cache document: %w", err)
	}

	return toolResultJSON(fetchResult{
		DocumentID:    documentID,
		LocalPath:     localPath,
		ContentType:   doc.ContentType,
		FileSizeBytes: written,
		Source:        "companies_house",
	})
}

// handleClearCache implements the clear_cache tool.
func (s *Server) handleClearCache(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber := req.GetString("ch_number", "")
	if chNumber != "" && !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}

	cleared, err := s.cache.Clear(ctx, chNumber)
	if err != nil {
		return nil, fmt.Errorf("clear cache: %w", err)
	}

	return toolResultJSON(clearCacheResult{
		DeletedFiles:     cleared.DeletedFiles,
		FreedBytes:       cleared.FreedBytes,
		DBRecordsRemoved: cleared.DBRecords,
	})
}

// zipEntryResult is a single entry in the list_zip_contents response.
type zipEntryResult struct {
	Filename      string `json:"filename"`
	LocalPath     string `json:"local_path"`
	ContentType   string `json:"content_type"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	IsPrimary     bool   `json:"is_primary"`
}

// listZipContentsResult is the response for list_zip_contents.
type listZipContentsResult struct {
	Entries        []zipEntryResult `json:"entries"`
	TotalInArchive int              `json:"total_in_archive,omitempty"`
	Truncated      bool             `json:"truncated,omitempty"`
}

// handleListZipContents implements the list_zip_contents tool.
func (s *Server) handleListZipContents(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chNumber, err := req.RequireString("ch_number")
	if err != nil {
		return toolError("ch_number is required")
	}
	if !companyhouse.ValidateCHNumber(chNumber) {
		return toolError("ch_number contains invalid characters")
	}

	documentID, err := req.RequireString("document_id")
	if err != nil {
		return toolError("document_id is required")
	}
	documentURL, refErr := s.cache.ResolveFilingRef(ctx, chNumber, documentID)
	if errors.Is(refErr, cache.ErrFilingRefNotFound) {
		return toolError("document_id not found; call list_filings or get_latest first to obtain a valid document_id")
	}
	if refErr != nil {
		return nil, fmt.Errorf("resolve filing ref: %w", refErr)
	}
	docID, ok := companyhouse.ParseDocumentID(documentURL)
	if !ok {
		return nil, fmt.Errorf("stored document_url does not contain a recognisable CH document ID: %s", documentURL)
	}
	if !companyhouse.ValidateDocID(docID) {
		return nil, fmt.Errorf("stored document_url contains an invalid document ID: %s", documentURL)
	}

	records, totalInArchive, err := s.cache.GetZipEntries(ctx, chNumber, docID)
	if err != nil {
		return nil, fmt.Errorf("get zip entries: %w", err)
	}
	if len(records) == 0 {
		return toolError("filing is not cached or was not extracted from a zip archive; call fetch_filing first")
	}

	entries := make([]zipEntryResult, len(records))
	for i, r := range records {
		entries[i] = zipEntryResult{
			Filename:      r.Filename,
			LocalPath:     r.LocalPath,
			ContentType:   r.ContentType,
			FileSizeBytes: r.FileSize,
			IsPrimary:     r.IsPrimary,
		}
	}
	return toolResultJSON(listZipContentsResult{
		Entries:        entries,
		TotalInArchive: totalInArchive,
		Truncated:      totalInArchive > len(records),
	})
}

// handleExtractXBRLFacts implements the extract_xbrl_facts tool.
func (s *Server) handleExtractXBRLFacts(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	localPath, err := req.RequireString("local_path")
	if err != nil {
		return toolError("local_path is required")
	}
	ext := strings.ToLower(filepath.Ext(localPath))
	if ext != ".xhtml" && ext != ".html" {
		return toolError("local_path must point to an .xhtml or .html file")
	}

	// Resolve symlinks and verify the path is within the cache file subtree.
	// This prevents reading arbitrary files or escaping via symlinks.
	realPath, pathErr := s.cache.ValidatePath(localPath)
	if errors.Is(pathErr, cache.ErrOutsideCache) {
		return toolError("local_path is not within the cache directory")
	}
	if pathErr != nil {
		return toolError("local_path does not point to a readable file")
	}

	info, statErr := os.Stat(realPath)
	if statErr != nil || !info.Mode().IsRegular() {
		return toolError("local_path does not point to a readable file")
	}

	opts := xbrl.Options{
		NamePrefix:       req.GetString("name_prefix", ""),
		IncludeTextFacts: req.GetBool("include_text_facts", false),
	}
	parsed, parseErr := xbrl.ParseFacts(realPath, opts)
	if parseErr != nil {
		return toolError(fmt.Sprintf("parse iXBRL: %s", parseErr))
	}
	res := xbrlFactsResult{
		Facts:      parsed.Facts,
		Count:      len(parsed.Facts),
		Truncated:  parsed.Truncated,
		RenderType: parsed.RenderType,
	}
	if parsed.RenderType == xbrl.RenderTypePDFRendered {
		res.Warnings = []string{buildPDFRenderedWarning(ctx, s.cache, realPath)}
	}
	return toolResultJSON(res)
}

// buildPDFRenderedWarning returns a warning string for PDF-rendered iXBRL files.
// If the file came from a zip archive it names any non-primary alternative entries;
// if no alternatives exist it says so; if not from a zip it falls back to the generic message.
func buildPDFRenderedWarning(ctx context.Context, c FilingCache, realPath string) string {
	chNumber, internalDocID, err := c.ParseFilingPath(realPath)
	if err != nil {
		return "narrative text is not reliably accessible in PDF-rendered iXBRL; consider fetching an alternative filing format"
	}
	records, _, err := c.GetZipEntries(ctx, chNumber, internalDocID)
	if err != nil || len(records) == 0 {
		return "narrative text is not reliably accessible in PDF-rendered iXBRL; consider fetching an alternative filing format"
	}
	var alts []string
	for _, r := range records {
		if !r.IsPrimary {
			alts = append(alts, fmt.Sprintf("%s (%s)", r.Filename, r.ContentType))
		}
	}
	if len(alts) == 0 {
		return "narrative text is not reliably accessible in PDF-rendered iXBRL; no alternative formats are available in the source archive"
	}
	return fmt.Sprintf(
		"narrative text is not reliably accessible in PDF-rendered iXBRL; %d alternative file(s) are available in the source archive: %s — use list_zip_contents with ch_number %q and the same document_id used to fetch this filing",
		len(alts), strings.Join(alts, ", "), chNumber,
	)
}

// xbrlFactsResult is the response envelope for extract_xbrl_facts.
// Truncated is true when the document contained more facts than the MaxFacts cap;
// callers should use name_prefix to narrow the query when this occurs.
// RenderType is "native_ixbrl" or "pdf_rendered"; Warnings is non-empty when
// narrative text is not reliably accessible.
type xbrlFactsResult struct {
	Facts      []xbrl.Fact `json:"facts"`
	Count      int         `json:"count"`
	Truncated  bool        `json:"truncated"`
	RenderType string      `json:"render_type"`
	Warnings   []string    `json:"warnings,omitempty"`
}

// toolResultForCHError maps CH sentinel errors to tool error results.
// Returns (errResult, nil) for known errors, (nil, wrappedErr) for unexpected errors.
func toolResultForCHError(err error, op string) (*mcp.CallToolResult, error) {
	if errors.Is(err, companyhouse.ErrNotFound) {
		return toolError("not found")
	}
	if errors.Is(err, companyhouse.ErrUnauthorized) {
		return toolError("CH API key invalid or missing")
	}
	if errors.Is(err, companyhouse.ErrRateLimited) {
		return toolError("CH rate limit hit, retry shortly")
	}
	return nil, fmt.Errorf("%s: %w", op, err)
}

// toolError wraps a user-facing message as a tool error result.
// MCP tool input and validation errors are signalled as tool error results (IsError=true),
// not as Go errors, so the MCP client receives a structured error rather than a transport failure.
func toolError(msg string) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(msg), nil
}

// toolResultJSON marshals v to JSON and wraps it in a text tool result.
func toolResultJSON(v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}
