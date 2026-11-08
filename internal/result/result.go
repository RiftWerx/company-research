// Package result defines the response types shared between the MCP and CLI layers.
package result

import "github.com/riftwerx/company-research/internal/xbrl"

// SearchResult is the per-company response for search_company / search-company.
type SearchResult struct {
	CHNumber string `json:"ch_number"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Type     string `json:"type"`
	Locality string `json:"locality,omitempty"`
}

// ProfileAddress is the address sub-object in ProfileResult.
type ProfileAddress struct {
	Line1    string `json:"line1,omitempty"`
	Line2    string `json:"line2,omitempty"`
	Locality string `json:"locality,omitempty"`
	Postcode string `json:"postcode,omitempty"`
	Country  string `json:"country,omitempty"`
}

// ProfileResult is the response for get_company_profile / get-company-profile.
type ProfileResult struct {
	CHNumber       string         `json:"ch_number"`
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	Type           string         `json:"type"`
	DateOfCreation string         `json:"date_of_creation,omitempty"`
	SICCodes       []string       `json:"sic_codes"`
	Address        ProfileAddress `json:"address"`
}

// FilingResult is the per-filing response for list_filings / list-filings.
type FilingResult struct {
	DocumentID  string `json:"document_id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Date        string `json:"date"` // YYYY-MM-DD
}

// ChargeClassificationResult is a charge classification in CompanyBriefResult.ActiveCharges.
type ChargeClassificationResult struct {
	Description string `json:"description"`
	Type        string `json:"type"`
}

// ChargeResult is a single entry in CompanyBriefResult.ActiveCharges.
type ChargeResult struct {
	Classification  ChargeClassificationResult `json:"classification"`
	Status          string                     `json:"status"`
	DeliveredOn     string                     `json:"delivered_on"`
	PersonsEntitled []string                   `json:"persons_entitled,omitempty"`
}

// PSCResult is a single entry in CompanyBriefResult.ActivePSC.
type PSCResult struct {
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	NaturesOfControl []string `json:"natures_of_control,omitempty"`
	NotifiedOn       string   `json:"notified_on"`
}

// InsolvencyDateResult is a single named milestone date within an InsolvencyCaseResult.
type InsolvencyDateResult struct {
	Type string `json:"type"`
	Date string `json:"date"`
}

// InsolvencyCaseResult is a single entry in CompanyBriefResult.InsolvencyCases.
type InsolvencyCaseResult struct {
	CaseType string                 `json:"case_type"`
	Number   string                 `json:"number,omitempty"`
	Dates    []InsolvencyDateResult `json:"dates,omitempty"`
}

// CompanyBriefResult is the response for get_company_brief / get-company-brief.
type CompanyBriefResult struct {
	Profile                     ProfileResult          `json:"profile"`
	LatestAccounts              *FilingResult          `json:"latest_accounts,omitempty"`
	LatestConfirmationStatement *FilingResult          `json:"latest_confirmation_statement,omitempty"`
	ActiveCharges               []ChargeResult         `json:"active_charges,omitempty"`
	ActivePSC                   []PSCResult            `json:"active_psc,omitempty"`
	InsolvencyCases             []InsolvencyCaseResult `json:"insolvency_cases,omitempty"`
	RecentFilings               []FilingResult         `json:"recent_filings"`
}

// FetchResult is the response for fetch_filing / fetch-filing and get_latest / get-latest.
type FetchResult struct {
	DocumentID     string `json:"document_id"`
	LocalPath      string `json:"local_path"`
	ContentType    string `json:"content_type"`
	FileSizeBytes  int64  `json:"file_size_bytes"`
	Source         string `json:"source"`
	IsArchive      bool   `json:"is_archive,omitempty"`
	TotalInArchive int    `json:"total_in_archive,omitempty"`
	Truncated      bool   `json:"truncated,omitempty"`
}

// ClearCacheResult is the response for clear_cache / clear-cache.
type ClearCacheResult struct {
	DeletedFiles     int64 `json:"deleted_files"`
	FreedBytes       int64 `json:"freed_bytes"`
	DBRecordsRemoved int64 `json:"db_records_removed"`
}

// ZipEntryResult is a single entry in ListZipContentsResult.
type ZipEntryResult struct {
	Filename      string `json:"filename"`
	LocalPath     string `json:"local_path"`
	ContentType   string `json:"content_type"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	IsPrimary     bool   `json:"is_primary"`
}

// ListZipContentsResult is the response for list_zip_contents / list-zip-contents.
type ListZipContentsResult struct {
	Entries        []ZipEntryResult `json:"entries"`
	TotalInArchive int              `json:"total_in_archive,omitempty"`
	Truncated      bool             `json:"truncated,omitempty"`
}

// XBRLFactsResult is the response for extract_xbrl_facts / extract-xbrl-facts.
// Truncated is true when the document contained more facts than the MaxFacts cap.
// RenderType is "native_ixbrl" or "pdf_rendered".
type XBRLFactsResult struct {
	Facts      []xbrl.Fact `json:"facts"`
	Count      int         `json:"count"`
	Truncated  bool        `json:"truncated"`
	RenderType string      `json:"render_type"`
	Warnings   []string    `json:"warnings,omitempty"`
}
