package companyhouse

import (
	"io"
	"time"
)

// RegisteredAddress is a company's registered office address.
type RegisteredAddress struct {
	AddressLine1 string
	AddressLine2 string
	Locality     string
	PostalCode   string
	Country      string
}

// SearchResult is a single company match returned by SearchCompanies.
type SearchResult struct {
	CompanyNumber  string
	Title          string
	CompanyType    string
	CompanyStatus  string
	DateOfCreation string
	Address        RegisteredAddress
}

// CompanyProfile is the full profile returned by GetCompanyProfile.
type CompanyProfile struct {
	CompanyNumber    string
	CompanyName      string
	CompanyType      string
	CompanyStatus    string
	DateOfCreation   string
	SICCodes         []string
	RegisteredOffice RegisteredAddress
}

// Filing is a single entry from the Companies House filing history.
type Filing struct {
	TransactionID string
	Type          string // e.g. "AA" (annual accounts), "CS01" (confirmation statement)
	Description   string
	Date          time.Time
	DocumentURL   string // full URL to the document metadata endpoint; pass to GetDocument
}

// ListFilingsOptions filters the filing history returned by GetFilingHistory.
// Zero values are omitted from the request.
type ListFilingsOptions struct {
	Category     string // e.g. "accounts", "confirmation-statement"
	StartIndex   int
	ItemsPerPage int
}

// Document holds the response from a document download.
// The caller is responsible for closing Body.
type Document struct {
	Body        io.ReadCloser
	ContentType string // e.g. "application/pdf" or "application/xhtml+xml"
}

// Filing history category values used by get_company_brief. Other handlers accept
// free-string category params and are not refactored to use these.
const (
	CategoryAccounts              = "accounts"
	CategoryConfirmationStatement = "confirmation-statement"
)

// Charge status values used by get_company_brief to filter for active charges.
// A charge may also be "satisfied" or "fully-satisfied", which are excluded.
const (
	ChargeStatusOutstanding   = "outstanding"
	ChargeStatusPartSatisfied = "part-satisfied"
)

// ChargeClassification is the classification object currently returned by the live Companies House API.
// The published schema describes this field as an array.
type ChargeClassification struct {
	Description string `json:"description"`
	Type        string `json:"type"`
}

// Charge is a single entry from a company's charges (mortgages/debentures) register.
type Charge struct {
	Classification  ChargeClassification
	Status          string // "outstanding", "part-satisfied", "satisfied", "fully-satisfied"
	DeliveredOn     string
	PersonsEntitled []string
}

// PSC is a single entry from a company's persons-with-significant-control register.
type PSC struct {
	Name             string
	Kind             string // e.g. "individual-person-with-significant-control", "corporate-entity-person-with-significant-control"
	NaturesOfControl []string
	NotifiedOn       string
	CeasedOn         string // empty if still active
	Ceased           bool   // CH sets this indicator for ceased super-secure PSCs
}

// InsolvencyDate is a single named milestone date within an InsolvencyCase.
type InsolvencyDate struct {
	Type string // e.g. "petitioned-on", "wound-up-on"
	Date string
}

// InsolvencyCase is a single entry from a company's insolvency register.
type InsolvencyCase struct {
	CaseType string // e.g. "compulsory-liquidation"
	Number   string // CH's per-company case number, e.g. "1"
	Dates    []InsolvencyDate
}
