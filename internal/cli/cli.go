// Package cli implements the CLI mode for company-research.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/alecthomas/kong"

	"github.com/riftwerx/company-research/internal/cache"
	"github.com/riftwerx/company-research/internal/companyhouse"
	"github.com/riftwerx/company-research/internal/version"
)

// CompanyHouseService is the subset of companyhouse.Service that CLI handlers require.
type CompanyHouseService interface {
	SearchCompanies(ctx context.Context, query string, maxResults int) ([]companyhouse.SearchResult, error)
	GetCompanyProfile(ctx context.Context, chNumber string) (*companyhouse.CompanyProfile, error)
	GetFilingHistory(ctx context.Context, chNumber string, opts companyhouse.ListFilingsOptions) ([]companyhouse.Filing, error)
	GetDocument(ctx context.Context, documentURL string) (*companyhouse.Document, error)
}

// FilingCache is the subset of cache.Cache that CLI handlers require.
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

// InstallSkillCmd implements the install-skill subcommand.
type InstallSkillCmd struct{}

// Run prints the skill install instructions.
func (c *InstallSkillCmd) Run() error {
	return fmt.Errorf("install-skill: not yet implemented")
}

// CLI is the root Kong command struct.
type CLI struct {
	Version           kong.VersionFlag     `name:"version" short:"V" help:"Print version and exit."`
	InstallSkill      InstallSkillCmd      `cmd:"" name:"install-skill" help:"Install the Claude skill definition."`
	SearchCompany     SearchCompanyCmd     `cmd:"" name:"search-company" help:"Search for a UK company by name."`
	GetCompanyProfile GetCompanyProfileCmd `cmd:"" name:"get-company-profile" help:"Get a company's profile."`
	ListFilings       ListFilingsCmd       `cmd:"" name:"list-filings" help:"List the filing history of a company."`
	FetchFiling       FetchFilingCmd       `cmd:"" name:"fetch-filing" help:"Download and cache a specific filing."`
	GetLatest         GetLatestCmd         `cmd:"" name:"get-latest" help:"Download the latest filing in a category."`
	ListZipContents   ListZipContentsCmd   `cmd:"" name:"list-zip-contents" help:"List the contents of a cached zip filing."`
	ExtractXBRLFacts  ExtractXBRLFactsCmd  `cmd:"" name:"extract-xbrl-facts" help:"Extract XBRL facts from a cached iXBRL file."`
	ClearCache        ClearCacheCmd        `cmd:"" name:"clear-cache" help:"Clear the local document cache."`
}

// Parse parses CLI arguments, handling --help, --version, and usage errors by exiting.
// It returns a context ready to execute when a valid subcommand is found.
// Services are not needed at this stage; pass them to Execute.
func Parse() *kong.Context {
	var cli CLI
	return kong.Parse(&cli,
		kong.Name("company-research"),
		kong.Description("UK company research tool."),
		kong.Vars{"version": version.Version},
		kong.UsageOnError(),
	)
}

// Execute runs the command selected by Parse, binding ctx and any additional
// bindings that the selected command's Run method requires.
// Bindings that implement CompanyHouseService or FilingCache are registered as
// those interface types so Kong can inject them into Run() parameters.
func Execute(ctx context.Context, kctx *kong.Context, bindings ...interface{}) error {
	kctx.BindTo(ctx, (*context.Context)(nil))
	for _, b := range bindings {
		switch v := b.(type) {
		case CompanyHouseService:
			kctx.BindTo(v, (*CompanyHouseService)(nil))
		case FilingCache:
			kctx.BindTo(v, (*FilingCache)(nil))
		}
	}
	return kctx.Run()
}
