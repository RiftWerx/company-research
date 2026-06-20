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
		date := ""
		if !f.Date.IsZero() {
			date = f.Date.Format("2006-01-02")
		}
		out = append(out, result.FilingResult{
			DocumentID:  docID,
			Type:        f.Type,
			Description: f.Description,
			Date:        date,
		})
	}
	return writeJSON(writerOf(c.out), out)
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
