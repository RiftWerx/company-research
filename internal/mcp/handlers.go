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
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/riftwerx/company-research/internal/archive"
	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
	"github.com/riftwerx/company-research/internal/result"
	"github.com/riftwerx/company-research/internal/xbrl"
)

// CompanyHouseService is the subset of companyhouse.Service that MCP handlers require.
type CompanyHouseService interface {
	SearchCompanies(ctx context.Context, query string, maxResults int) ([]companyhouse.SearchResult, error)
	GetCompanyProfile(ctx context.Context, chNumber string) (*companyhouse.CompanyProfile, error)
	GetFilingHistory(ctx context.Context, chNumber string, opts companyhouse.ListFilingsOptions) ([]companyhouse.Filing, error)
	GetDocument(ctx context.Context, documentURL string) (*companyhouse.Document, error)
	GetCharges(ctx context.Context, chNumber string) ([]companyhouse.Charge, error)
	GetPersonsWithSignificantControl(ctx context.Context, chNumber string) ([]companyhouse.PSC, error)
	GetInsolvencyCases(ctx context.Context, chNumber string) ([]companyhouse.InsolvencyCase, error)
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

// briefCandidateLimit is the max search candidates fetched/shown when get_company_brief
// must disambiguate a company_name that matched more than one company.
const briefCandidateLimit = 5

// defaultBriefRecentLimit is the default cap on get_company_brief's recent_filings list.
const defaultBriefRecentLimit = 20

// maxBriefRecentLimit is the maximum allowed value for get_company_brief's limit param.
const maxBriefRecentLimit = 40

// briefRecentFetchPadding is added to limit when fetching recent filings, to absorb the
// up-to-2 entries excluded because they duplicate latest_accounts/latest_confirmation_statement,
// so the final recent_filings list still reaches limit where the upstream history allows it.
const briefRecentFetchPadding = 2

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

	out := make([]result.SearchResult, len(results))
	for i, r := range results {
		out[i] = result.SearchResult{
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
	out := result.ProfileResult{
		CHNumber:       profile.CompanyNumber,
		Name:           profile.CompanyName,
		Status:         profile.CompanyStatus,
		Type:           profile.CompanyType,
		DateOfCreation: profile.DateOfCreation,
		SICCodes:       sicCodes,
		Address: result.ProfileAddress{
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
	out := make([]result.FilingResult, 0, len(filings))
	for _, f := range filings {
		if f.DocumentURL == "" {
			continue
		}
		docID, refErr := s.cache.StoreFilingRef(ctx, chNumber, f.TransactionID, f.DocumentURL)
		if refErr != nil {
			return nil, fmt.Errorf("store filing ref: %w", refErr)
		}
		out = append(out, result.FilingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        formatFilingDate(f.Date),
		})
	}
	return toolResultJSON(out)
}

// formatFilingDate renders a filing date as YYYY-MM-DD, or "" if unset.
func formatFilingDate(d time.Time) string {
	if d.IsZero() {
		return ""
	}
	return d.Format("2006-01-02")
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

// handleGetCompanyBrief implements the get_company_brief tool.
func (s *Server) handleGetCompanyBrief(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	companyName := req.GetString("company_name", "")
	chNumber := req.GetString("ch_number", "")
	if companyName == "" && chNumber == "" {
		return toolError("company_name or ch_number is required")
	}
	limit := req.GetInt("limit", defaultBriefRecentLimit)
	switch {
	case limit <= 0:
		limit = defaultBriefRecentLimit
	case limit > maxBriefRecentLimit:
		limit = maxBriefRecentLimit
	}

	if chNumber != "" {
		if !companyhouse.ValidateCHNumber(chNumber) {
			return toolError("ch_number contains invalid characters")
		}
	} else {
		candidates, err := s.chSvc.SearchCompanies(ctx, companyName, briefCandidateLimit)
		if err != nil {
			return toolResultForCHError(err, "search companies")
		}
		switch len(candidates) {
		case 0:
			return toolError(fmt.Sprintf("no company found matching %q", companyName))
		case 1:
			chNumber = candidates[0].CompanyNumber
		default:
			return toolError(formatBriefCandidates(companyName, candidates))
		}
		if !companyhouse.ValidateCHNumber(chNumber) {
			return toolError("ch_number contains invalid characters")
		}
	}

	profile, err := s.chSvc.GetCompanyProfile(ctx, chNumber)
	if err != nil {
		return toolResultForCHError(err, "get company profile")
	}
	sicCodes := profile.SICCodes
	if sicCodes == nil {
		sicCodes = []string{}
	}
	// Filing references must use the same canonical number returned to callers.
	if !companyhouse.ValidateCHNumber(profile.CompanyNumber) {
		return nil, fmt.Errorf("get company brief: profile contains an invalid company number")
	}
	chNumber = profile.CompanyNumber
	out := result.CompanyBriefResult{
		Profile: result.ProfileResult{
			CHNumber:       profile.CompanyNumber,
			Name:           profile.CompanyName,
			Status:         profile.CompanyStatus,
			Type:           profile.CompanyType,
			DateOfCreation: profile.DateOfCreation,
			SICCodes:       sicCodes,
			Address: result.ProfileAddress{
				Line1:    profile.RegisteredOffice.AddressLine1,
				Line2:    profile.RegisteredOffice.AddressLine2,
				Locality: profile.RegisteredOffice.Locality,
				Postcode: profile.RegisteredOffice.PostalCode,
				Country:  profile.RegisteredOffice.Country,
			},
		},
	}

	var latestAccountsTxID, latestConfirmationTxID string

	accounts, err := s.chSvc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     companyhouse.CategoryAccounts,
		ItemsPerPage: 1,
	})
	if err != nil {
		return toolResultForCHError(err, "list filings")
	}
	if len(accounts) > 0 && accounts[0].DocumentURL != "" {
		docID, refErr := s.cache.StoreFilingRef(ctx, chNumber, accounts[0].TransactionID, accounts[0].DocumentURL)
		if refErr != nil {
			return nil, fmt.Errorf("store filing ref: %w", refErr)
		}
		out.LatestAccounts = &result.FilingResult{
			DocumentID:  docID,
			Type:        accounts[0].Type,
			Description: accounts[0].Description,
			Date:        formatFilingDate(accounts[0].Date),
		}
		latestAccountsTxID = accounts[0].TransactionID
	}

	confirmation, err := s.chSvc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     companyhouse.CategoryConfirmationStatement,
		ItemsPerPage: 1,
	})
	if err != nil {
		return toolResultForCHError(err, "list filings")
	}
	if len(confirmation) > 0 && confirmation[0].DocumentURL != "" {
		docID, refErr := s.cache.StoreFilingRef(ctx, chNumber, confirmation[0].TransactionID, confirmation[0].DocumentURL)
		if refErr != nil {
			return nil, fmt.Errorf("store filing ref: %w", refErr)
		}
		out.LatestConfirmationStatement = &result.FilingResult{
			DocumentID:  docID,
			Type:        confirmation[0].Type,
			Description: confirmation[0].Description,
			Date:        formatFilingDate(confirmation[0].Date),
		}
		latestConfirmationTxID = confirmation[0].TransactionID
	}

	charges, err := s.chSvc.GetCharges(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return toolResultForCHError(err, "get charges")
	}
	for _, c := range charges {
		if c.Status != companyhouse.ChargeStatusOutstanding && c.Status != companyhouse.ChargeStatusPartSatisfied {
			continue
		}
		out.ActiveCharges = append(out.ActiveCharges, result.ChargeResult{
			Classification: result.ChargeClassificationResult{
				Description: c.Classification.Description,
				Type:        c.Classification.Type,
			},
			Status:          c.Status,
			DeliveredOn:     c.DeliveredOn,
			PersonsEntitled: c.PersonsEntitled,
		})
	}

	pscs, err := s.chSvc.GetPersonsWithSignificantControl(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return toolResultForCHError(err, "get persons with significant control")
	}
	for _, p := range pscs {
		if p.Ceased || p.CeasedOn != "" {
			continue
		}
		out.ActivePSC = append(out.ActivePSC, result.PSCResult{
			Name:             p.Name,
			Kind:             p.Kind,
			NaturesOfControl: p.NaturesOfControl,
			NotifiedOn:       p.NotifiedOn,
		})
	}

	insolvencyCases, err := s.chSvc.GetInsolvencyCases(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return toolResultForCHError(err, "get insolvency cases")
	}
	for _, ic := range insolvencyCases {
		dates := make([]result.InsolvencyDateResult, len(ic.Dates))
		for i, d := range ic.Dates {
			dates[i] = result.InsolvencyDateResult{Type: d.Type, Date: d.Date}
		}
		out.InsolvencyCases = append(out.InsolvencyCases, result.InsolvencyCaseResult{
			CaseType: ic.CaseType,
			Number:   ic.Number,
			Dates:    dates,
		})
	}

	// Fetch more than limit: latest_accounts and latest_confirmation_statement are excluded
	// below, so an unpadded fetch could return fewer than limit entries even when the
	// company has more recent filings just outside the unpadded window.
	recent, err := s.chSvc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{ItemsPerPage: limit + briefRecentFetchPadding})
	if err != nil {
		return toolResultForCHError(err, "list filings")
	}
	out.RecentFilings = make([]result.FilingResult, 0, limit)
	for _, f := range recent {
		if len(out.RecentFilings) >= limit {
			break
		}
		if f.DocumentURL == "" {
			continue
		}
		if f.TransactionID != "" && (f.TransactionID == latestAccountsTxID || f.TransactionID == latestConfirmationTxID) {
			continue
		}
		docID, refErr := s.cache.StoreFilingRef(ctx, chNumber, f.TransactionID, f.DocumentURL)
		if refErr != nil {
			return nil, fmt.Errorf("store filing ref: %w", refErr)
		}
		out.RecentFilings = append(out.RecentFilings, result.FilingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        formatFilingDate(f.Date),
		})
	}

	return toolResultJSON(out)
}

// formatBriefCandidates builds a disambiguation message listing search candidates by
// ch_number, name, status, and type, instructing the caller to retry with ch_number.
func formatBriefCandidates(companyName string, candidates []companyhouse.SearchResult) string {
	lines := make([]string, len(candidates))
	for i, c := range candidates {
		lines[i] = fmt.Sprintf("%s: %s (%s, %s)", c.CompanyNumber, c.Title, c.CompanyStatus, c.CompanyType)
	}
	return fmt.Sprintf("multiple companies match %q; retry with an explicit ch_number:\n%s", companyName, strings.Join(lines, "\n"))
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
		res := result.FetchResult{
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
		return toolResultJSON(result.FetchResult{
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

	return toolResultJSON(result.FetchResult{
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

	return toolResultJSON(result.ClearCacheResult{
		DeletedFiles:     cleared.DeletedFiles,
		FreedBytes:       cleared.FreedBytes,
		DBRecordsRemoved: cleared.DBRecords,
	})
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

	entries := make([]result.ZipEntryResult, len(records))
	for i, r := range records {
		entries[i] = result.ZipEntryResult{
			Filename:      r.Filename,
			LocalPath:     r.LocalPath,
			ContentType:   r.ContentType,
			FileSizeBytes: r.FileSize,
			IsPrimary:     r.IsPrimary,
		}
	}
	return toolResultJSON(result.ListZipContentsResult{
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
	res := result.XBRLFactsResult{
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
