package cli

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

	"github.com/riftwerx/company-research/internal/archive"
	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
	"github.com/riftwerx/company-research/internal/result"
	"github.com/riftwerx/company-research/internal/xbrl"
)

// SearchCompanyCmd implements the search-company subcommand.
type SearchCompanyCmd struct {
	Query string `arg:"" name:"query" help:"Search term."`
	Limit int    `name:"limit" short:"l" default:"10" help:"Maximum number of results."`
	out   io.Writer
}

// Run searches for a company by name and writes JSON results to stdout.
func (c *SearchCompanyCmd) Run(ctx context.Context, svc CompanyHouseService) error {
	results, err := svc.SearchCompanies(ctx, c.Query, c.Limit)
	if err != nil {
		return chError(err, "search companies")
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
	return writeJSON(writerOf(c.out), out)
}

// GetCompanyProfileCmd implements the get-company-profile subcommand.
type GetCompanyProfileCmd struct {
	CHNumber string `arg:"" name:"ch-number" help:"Companies House number (e.g. 00445790)."`
	out      io.Writer
}

// Run fetches a company profile and writes JSON to stdout.
func (c *GetCompanyProfileCmd) Run(ctx context.Context, svc CompanyHouseService) error {
	if !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	profile, err := svc.GetCompanyProfile(ctx, c.CHNumber)
	if err != nil {
		return chError(err, "get company profile")
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
	return writeJSON(writerOf(c.out), out)
}

// ListFilingsCmd implements the list-filings subcommand.
type ListFilingsCmd struct {
	CHNumber string `arg:"" name:"ch-number" help:"Companies House number."`
	Category string `name:"category" short:"c" help:"Filing category filter (e.g. accounts)."`
	Start    int    `name:"start" default:"0" help:"Start index for pagination."`
	Limit    int    `name:"limit" short:"l" default:"20" help:"Maximum number of filings."`
	out      io.Writer
}

// Run lists filings for a company and writes JSON to stdout.
func (c *ListFilingsCmd) Run(ctx context.Context, svc CompanyHouseService, fc FilingCache) error {
	if !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	filings, err := svc.GetFilingHistory(ctx, c.CHNumber, companyhouse.ListFilingsOptions{
		Category:     c.Category,
		StartIndex:   c.Start,
		ItemsPerPage: c.Limit,
	})
	if err != nil {
		return chError(err, "list filings")
	}
	// Omit filings with no downloadable document.
	out := make([]result.FilingResult, 0, len(filings))
	for _, f := range filings {
		if f.DocumentURL == "" {
			continue
		}
		docID, refErr := fc.StoreFilingRef(ctx, c.CHNumber, f.TransactionID, f.DocumentURL)
		if refErr != nil {
			return fmt.Errorf("store filing ref: %w", refErr)
		}
		out = append(out, result.FilingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        formatFilingDate(f.Date),
		})
	}
	return writeJSON(writerOf(c.out), out)
}

// formatFilingDate renders a filing date as YYYY-MM-DD, or "" if unset.
func formatFilingDate(d time.Time) string {
	if d.IsZero() {
		return ""
	}
	return d.Format("2006-01-02")
}

// FetchFilingCmd implements the fetch-filing subcommand.
type FetchFilingCmd struct {
	CHNumber   string `arg:"" name:"ch-number" help:"Companies House number."`
	DocumentID string `arg:"" name:"document-id" help:"Opaque document ID from list-filings output."`
	out        io.Writer
}

// Run downloads and caches a filing, writing a JSON fetch result to stdout.
func (c *FetchFilingCmd) Run(ctx context.Context, svc CompanyHouseService, fc FilingCache) error {
	if !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	documentURL, refErr := fc.ResolveFilingRef(ctx, c.CHNumber, c.DocumentID)
	if errors.Is(refErr, cache.ErrFilingRefNotFound) {
		return fmt.Errorf("document-id not found; call list-filings or get-latest first to obtain a valid document-id")
	}
	if refErr != nil {
		return fmt.Errorf("resolve filing ref: %w", refErr)
	}
	res, err := fetchDocument(ctx, c.CHNumber, documentURL, c.DocumentID, svc, fc)
	if err != nil {
		return err
	}
	return writeJSON(writerOf(c.out), res)
}

// GetLatestCmd implements the get-latest subcommand.
type GetLatestCmd struct {
	CHNumber string `arg:"" name:"ch-number" help:"Companies House number."`
	Category string `arg:"" name:"category" help:"Filing category (e.g. accounts)."`
	out      io.Writer
}

// Run downloads the most recent filing in a category and writes JSON to stdout.
func (c *GetLatestCmd) Run(ctx context.Context, svc CompanyHouseService, fc FilingCache) error {
	if !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	filings, err := svc.GetFilingHistory(ctx, c.CHNumber, companyhouse.ListFilingsOptions{
		Category:     c.Category,
		ItemsPerPage: 1,
	})
	if err != nil {
		return chError(err, "list filings")
	}
	if len(filings) == 0 {
		return fmt.Errorf("no filings found for that category")
	}
	if filings[0].DocumentURL == "" {
		return fmt.Errorf("most recent filing in that category has no downloadable document")
	}
	documentID, refErr := fc.StoreFilingRef(ctx, c.CHNumber, filings[0].TransactionID, filings[0].DocumentURL)
	if refErr != nil {
		return fmt.Errorf("store filing ref: %w", refErr)
	}
	res, err := fetchDocument(ctx, c.CHNumber, filings[0].DocumentURL, documentID, svc, fc)
	if err != nil {
		return err
	}
	return writeJSON(writerOf(c.out), res)
}

// briefCandidateLimit is the max search candidates fetched/shown when get-company-brief
// must disambiguate a company-name that matched more than one company.
const briefCandidateLimit = 5

// defaultBriefRecentLimit is the default cap on get-company-brief's recent-filings list.
const defaultBriefRecentLimit = 20

// maxBriefRecentLimit is the maximum allowed value for get-company-brief's --limit flag.
const maxBriefRecentLimit = 40

// briefRecentFetchPadding is added to limit when fetching recent filings, to absorb the
// up-to-2 entries excluded because they duplicate latest_accounts/latest_confirmation_statement,
// so the final recent-filings list still reaches limit where the upstream history allows it.
const briefRecentFetchPadding = 2

// GetCompanyBriefCmd implements the get-company-brief subcommand.
type GetCompanyBriefCmd struct {
	CompanyName string `name:"company-name" help:"Company name to search for. Required if --ch-number is omitted."`
	CHNumber    string `name:"ch-number" help:"Companies House number, if already known. Skips the name search."`
	Limit       int    `name:"limit" short:"l" default:"20" help:"Maximum number of recent filings (capped at 40)."`
	out         io.Writer
}

// Run resolves a company by name or number and writes a JSON company brief to stdout:
// profile, latest accounts, latest confirmation statement, active charges, active persons
// with significant control, recorded insolvency cases, and a bounded recent-filings list.
func (c *GetCompanyBriefCmd) Run(ctx context.Context, svc CompanyHouseService, fc FilingCache) error {
	if c.CompanyName == "" && c.CHNumber == "" {
		return fmt.Errorf("company-name or ch-number is required")
	}
	limit := c.Limit
	switch {
	case limit <= 0:
		limit = defaultBriefRecentLimit
	case limit > maxBriefRecentLimit:
		limit = maxBriefRecentLimit
	}

	chNumber := c.CHNumber
	if chNumber != "" {
		if !companyhouse.ValidateCHNumber(chNumber) {
			return fmt.Errorf("ch-number contains invalid characters")
		}
	} else {
		candidates, err := svc.SearchCompanies(ctx, c.CompanyName, briefCandidateLimit)
		if err != nil {
			return chError(err, "search companies")
		}
		switch len(candidates) {
		case 0:
			return fmt.Errorf("no company found matching %q", c.CompanyName)
		case 1:
			chNumber = candidates[0].CompanyNumber
		default:
			return fmt.Errorf("%s", formatBriefCandidates(c.CompanyName, candidates))
		}
		if !companyhouse.ValidateCHNumber(chNumber) {
			return fmt.Errorf("ch-number contains invalid characters")
		}
	}

	profile, err := svc.GetCompanyProfile(ctx, chNumber)
	if err != nil {
		return chError(err, "get company profile")
	}
	sicCodes := profile.SICCodes
	if sicCodes == nil {
		sicCodes = []string{}
	}
	// Filing references must use the same canonical number returned to callers.
	if !companyhouse.ValidateCHNumber(profile.CompanyNumber) {
		return fmt.Errorf("get company brief: profile contains an invalid company number")
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

	accounts, err := svc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     companyhouse.CategoryAccounts,
		ItemsPerPage: 1,
	})
	if err != nil {
		return chError(err, "list filings")
	}
	if len(accounts) > 0 && accounts[0].DocumentURL != "" {
		docID, refErr := fc.StoreFilingRef(ctx, chNumber, accounts[0].TransactionID, accounts[0].DocumentURL)
		if refErr != nil {
			return fmt.Errorf("store filing ref: %w", refErr)
		}
		out.LatestAccounts = &result.FilingResult{
			DocumentID:  docID,
			Type:        accounts[0].Type,
			Description: accounts[0].Description,
			Date:        formatFilingDate(accounts[0].Date),
		}
		latestAccountsTxID = accounts[0].TransactionID
	}

	confirmation, err := svc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{
		Category:     companyhouse.CategoryConfirmationStatement,
		ItemsPerPage: 1,
	})
	if err != nil {
		return chError(err, "list filings")
	}
	if len(confirmation) > 0 && confirmation[0].DocumentURL != "" {
		docID, refErr := fc.StoreFilingRef(ctx, chNumber, confirmation[0].TransactionID, confirmation[0].DocumentURL)
		if refErr != nil {
			return fmt.Errorf("store filing ref: %w", refErr)
		}
		out.LatestConfirmationStatement = &result.FilingResult{
			DocumentID:  docID,
			Type:        confirmation[0].Type,
			Description: confirmation[0].Description,
			Date:        formatFilingDate(confirmation[0].Date),
		}
		latestConfirmationTxID = confirmation[0].TransactionID
	}

	charges, err := svc.GetCharges(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return chError(err, "get charges")
	}
	for _, ch := range charges {
		if ch.Status != companyhouse.ChargeStatusOutstanding && ch.Status != companyhouse.ChargeStatusPartSatisfied {
			continue
		}
		out.ActiveCharges = append(out.ActiveCharges, result.ChargeResult{
			Classification: result.ChargeClassificationResult{
				Description: ch.Classification.Description,
				Type:        ch.Classification.Type,
			},
			Status:          ch.Status,
			DeliveredOn:     ch.DeliveredOn,
			PersonsEntitled: ch.PersonsEntitled,
		})
	}

	pscs, err := svc.GetPersonsWithSignificantControl(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return chError(err, "get persons with significant control")
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

	insolvencyCases, err := svc.GetInsolvencyCases(ctx, chNumber)
	if err != nil && !errors.Is(err, companyhouse.ErrNotFound) {
		return chError(err, "get insolvency cases")
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
	recent, err := svc.GetFilingHistory(ctx, chNumber, companyhouse.ListFilingsOptions{ItemsPerPage: limit + briefRecentFetchPadding})
	if err != nil {
		return chError(err, "list filings")
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
		docID, refErr := fc.StoreFilingRef(ctx, chNumber, f.TransactionID, f.DocumentURL)
		if refErr != nil {
			return fmt.Errorf("store filing ref: %w", refErr)
		}
		out.RecentFilings = append(out.RecentFilings, result.FilingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        formatFilingDate(f.Date),
		})
	}

	return writeJSON(writerOf(c.out), out)
}

// formatBriefCandidates builds a disambiguation message listing search candidates by
// ch_number, name, status, and type, instructing the caller to retry with --ch-number.
func formatBriefCandidates(companyName string, candidates []companyhouse.SearchResult) string {
	lines := make([]string, len(candidates))
	for i, c := range candidates {
		lines[i] = fmt.Sprintf("%s: %s (%s, %s)", c.CompanyNumber, c.Title, c.CompanyStatus, c.CompanyType)
	}
	return fmt.Sprintf("multiple companies match %q; retry with an explicit --ch-number:\n%s", companyName, strings.Join(lines, "\n"))
}

// ListZipContentsCmd implements the list-zip-contents subcommand.
type ListZipContentsCmd struct {
	CHNumber   string `arg:"" name:"ch-number" help:"Companies House number."`
	DocumentID string `arg:"" name:"document-id" help:"Opaque document ID from list-filings output."`
	out        io.Writer
}

// Run lists the entries in a cached zip filing and writes JSON to stdout.
func (c *ListZipContentsCmd) Run(ctx context.Context, fc FilingCache) error {
	if !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	documentURL, refErr := fc.ResolveFilingRef(ctx, c.CHNumber, c.DocumentID)
	if errors.Is(refErr, cache.ErrFilingRefNotFound) {
		return fmt.Errorf("document-id not found; call list-filings or get-latest first to obtain a valid document-id")
	}
	if refErr != nil {
		return fmt.Errorf("resolve filing ref: %w", refErr)
	}
	docID, ok := companyhouse.ParseDocumentID(documentURL)
	if !ok {
		return fmt.Errorf("stored document_url does not contain a recognisable CH document ID: %s", documentURL)
	}
	if !companyhouse.ValidateDocID(docID) {
		return fmt.Errorf("stored document_url contains an invalid document ID: %s", documentURL)
	}
	records, totalInArchive, err := fc.GetZipEntries(ctx, c.CHNumber, docID)
	if err != nil {
		return fmt.Errorf("get zip entries: %w", err)
	}
	if len(records) == 0 {
		return fmt.Errorf("filing is not cached or was not extracted from a zip archive; call fetch-filing first")
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
	return writeJSON(writerOf(c.out), result.ListZipContentsResult{
		Entries:        entries,
		TotalInArchive: totalInArchive,
		Truncated:      totalInArchive > len(records),
	})
}

// ExtractXBRLFactsCmd implements the extract-xbrl-facts subcommand.
type ExtractXBRLFactsCmd struct {
	LocalPath        string `arg:"" name:"local-path" help:"Path to a cached .xhtml or .html iXBRL file."`
	NamePrefix       string `name:"name-prefix" help:"Filter facts by XBRL name prefix."`
	IncludeTextFacts bool   `name:"include-text-facts" help:"Include text (non-numeric) facts."`
	out              io.Writer
}

// Run extracts XBRL facts from a cached iXBRL file and writes JSON to stdout.
func (c *ExtractXBRLFactsCmd) Run(ctx context.Context, fc FilingCache) error {
	ext := strings.ToLower(filepath.Ext(c.LocalPath))
	if ext != ".xhtml" && ext != ".html" {
		return fmt.Errorf("local-path must point to an .xhtml or .html file")
	}
	realPath, pathErr := fc.ValidatePath(c.LocalPath)
	if errors.Is(pathErr, cache.ErrOutsideCache) {
		return fmt.Errorf("local-path is not within the cache directory")
	}
	if pathErr != nil {
		return fmt.Errorf("local-path does not point to a readable file")
	}
	info, statErr := os.Stat(realPath)
	if statErr != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("local-path does not point to a readable file")
	}
	opts := xbrl.Options{
		NamePrefix:       c.NamePrefix,
		IncludeTextFacts: c.IncludeTextFacts,
	}
	parsed, parseErr := xbrl.ParseFacts(realPath, opts)
	if parseErr != nil {
		return fmt.Errorf("parse iXBRL: %s", parseErr)
	}
	res := result.XBRLFactsResult{
		Facts:      parsed.Facts,
		Count:      len(parsed.Facts),
		Truncated:  parsed.Truncated,
		RenderType: parsed.RenderType,
	}
	if parsed.RenderType == xbrl.RenderTypePDFRendered {
		res.Warnings = []string{buildPDFRenderedWarning(ctx, fc, realPath)}
	}
	return writeJSON(writerOf(c.out), res)
}

// ClearCacheCmd implements the clear-cache subcommand.
type ClearCacheCmd struct {
	CHNumber string `name:"ch-number" help:"Limit clearing to this company (optional)."`
	out      io.Writer
}

// Run clears the local cache and writes a JSON summary to stdout.
func (c *ClearCacheCmd) Run(ctx context.Context, fc FilingCache) error {
	if c.CHNumber != "" && !companyhouse.ValidateCHNumber(c.CHNumber) {
		return fmt.Errorf("ch-number contains invalid characters")
	}
	cleared, err := fc.Clear(ctx, c.CHNumber)
	if err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}
	return writeJSON(writerOf(c.out), result.ClearCacheResult{
		DeletedFiles:     cleared.DeletedFiles,
		FreedBytes:       cleared.FreedBytes,
		DBRecordsRemoved: cleared.DBRecords,
	})
}

// fetchDocument retrieves a filing from the cache or downloads it from CH.
// documentURL is a trusted CH document API URL resolved from the filing_refs table.
// documentID is the opaque UUID to include in the response.
func fetchDocument(ctx context.Context, chNumber, documentURL, documentID string, svc CompanyHouseService, fc FilingCache) (result.FetchResult, error) {
	// These URL validations guard against corrupt DB values, not user input.
	if !companyhouse.ValidateDocumentURL(documentURL) {
		return result.FetchResult{}, fmt.Errorf("stored document_url is not a valid CH document API URL: %s", documentURL)
	}
	docID, ok := companyhouse.ParseDocumentID(documentURL)
	if !ok {
		return result.FetchResult{}, fmt.Errorf("stored document_url does not contain a recognisable CH document ID: %s", documentURL)
	}
	if !companyhouse.ValidateDocID(docID) {
		return result.FetchResult{}, fmt.Errorf("stored document_url contains an invalid document ID: %s", documentURL)
	}

	entry, err := fc.Get(ctx, chNumber, docID)
	if err != nil {
		return result.FetchResult{}, fmt.Errorf("check cache: %w", err)
	}
	if entry != nil {
		res := result.FetchResult{
			DocumentID:    documentID,
			LocalPath:     entry.LocalPath,
			ContentType:   entry.ContentType,
			FileSizeBytes: entry.FileSize,
			Source:        "cache",
		}
		zipRecords, totalInArchive, zipErr := fc.GetZipEntries(ctx, chNumber, docID)
		if zipErr != nil {
			return result.FetchResult{}, fmt.Errorf("check zip entries: %w", zipErr)
		}
		if len(zipRecords) > 0 {
			res.IsArchive = true
			res.TotalInArchive = totalInArchive
			res.Truncated = totalInArchive > len(zipRecords)
		}
		return res, nil
	}

	doc, err := svc.GetDocument(ctx, documentURL)
	if err != nil {
		return result.FetchResult{}, chError(err, "fetch document")
	}
	defer doc.Body.Close()

	// Detect zip by Content-Type first; fall back to magic bytes PK\x03\x04.
	peek := make([]byte, 4)
	n, _ := io.ReadFull(doc.Body, peek)
	peeked := peek[:n]
	isZip := doc.ContentType == "application/zip" ||
		(n >= 4 && peeked[0] == 'P' && peeked[1] == 'K' && peeked[2] == 0x03 && peeked[3] == 0x04)
	doc.Body = io.NopCloser(io.MultiReader(bytes.NewReader(peeked), doc.Body))

	if isZip {
		zipData, readErr := archive.ReadBody(doc.Body, cache.MaxFileSizeBytes)
		if errors.Is(readErr, archive.ErrBodyTooLarge) {
			return result.FetchResult{}, fmt.Errorf("zip filing exceeds %d-byte size limit", cache.MaxFileSizeBytes)
		}
		if readErr != nil {
			return result.FetchResult{}, fmt.Errorf("read zip: %w", readErr)
		}
		zipEntries, totalInArchive, extractErr := archive.ExtractAll(zipData, cache.MaxFileSizeBytes)
		if extractErr != nil {
			return result.FetchResult{}, fmt.Errorf("unpack zip: %s", extractErr)
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
		primaryPath, cacheErr := fc.PutZipEntries(ctx, chNumber, docID, cacheEntries, totalInArchive)
		if cacheErr != nil {
			return result.FetchResult{}, fmt.Errorf("cache zip entries: %w", cacheErr)
		}
		primary := zipEntries[0] // ExtractAll guarantees primary is first
		return result.FetchResult{
			DocumentID:     documentID,
			LocalPath:      primaryPath,
			ContentType:    primary.ContentType,
			FileSizeBytes:  int64(len(primary.Content)),
			Source:         "companies_house",
			IsArchive:      true,
			TotalInArchive: totalInArchive,
			Truncated:      totalInArchive > len(zipEntries),
		}, nil
	}

	localPath, written, err := fc.Put(ctx, chNumber, docID, doc.ContentType, "", doc.Body)
	if err != nil {
		return result.FetchResult{}, fmt.Errorf("cache document: %w", err)
	}
	return result.FetchResult{
		DocumentID:    documentID,
		LocalPath:     localPath,
		ContentType:   doc.ContentType,
		FileSizeBytes: written,
		Source:        "companies_house",
	}, nil
}

// buildPDFRenderedWarning returns a warning string for PDF-rendered iXBRL files.
func buildPDFRenderedWarning(ctx context.Context, fc FilingCache, realPath string) string {
	chNumber, internalDocID, err := fc.ParseFilingPath(realPath)
	if err != nil {
		return "narrative text is not reliably accessible in PDF-rendered iXBRL; consider fetching an alternative filing format"
	}
	records, _, err := fc.GetZipEntries(ctx, chNumber, internalDocID)
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
		"narrative text is not reliably accessible in PDF-rendered iXBRL; %d alternative file(s) are available in the source archive: %s — use list-zip-contents with ch-number %q and the same document-id used to fetch this filing",
		len(alts), strings.Join(alts, ", "), chNumber,
	)
}

// writerOf returns w if non-nil, otherwise os.Stdout.
func writerOf(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stdout
}

// writeJSON marshals v to JSON and writes it with a trailing newline to w.
func writeJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// chError translates companyhouse sentinel errors into user-facing errors.
func chError(err error, op string) error {
	if errors.Is(err, companyhouse.ErrNotFound) {
		return fmt.Errorf("not found")
	}
	if errors.Is(err, companyhouse.ErrUnauthorized) {
		return fmt.Errorf("CH API key invalid or missing")
	}
	if errors.Is(err, companyhouse.ErrRateLimited) {
		return fmt.Errorf("CH rate limit hit, retry shortly")
	}
	return fmt.Errorf("%s: %w", op, err)
}
