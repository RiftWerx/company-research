package mcp

import "github.com/mark3labs/mcp-go/mcp"

// searchCompanyTool returns the tool definition for search_company.
func searchCompanyTool() mcp.Tool {
	return mcp.NewTool("search_company",
		mcp.WithDescription("Search for a UK company by name. Returns a list of matching companies with their Companies House numbers."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Company name or partial name to search for"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return (default 10)"),
		),
	)
}

// getCompanyProfileTool returns the tool definition for get_company_profile.
func getCompanyProfileTool() mcp.Tool {
	return mcp.NewTool("get_company_profile",
		mcp.WithDescription("Get the profile of a UK company by its Companies House number. Returns name, status, type, SIC codes, registered address, and date of incorporation."),
		mcp.WithString("ch_number",
			mcp.Required(),
			mcp.Description("Companies House number (e.g. '00445790'). Zero-padded to 8 digits."),
		),
	)
}

// listFilingsTool returns the tool definition for list_filings.
func listFilingsTool() mcp.Tool {
	return mcp.NewTool("list_filings",
		mcp.WithDescription("List the filing history for a UK company. Returns [{document_id, type, description, date}] — pass document_id to fetch_filing or list_zip_contents. Filings without a downloadable document are excluded."),
		mcp.WithString("ch_number",
			mcp.Required(),
			mcp.Description("Companies House number"),
		),
		mcp.WithString("category",
			mcp.Description("Filter by filing category. Common values: 'accounts', 'confirmation-statement'. Omit to return all categories."),
		),
		mcp.WithNumber("start",
			mcp.Description("Pagination offset — index of the first result to return (default 0)"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of filings to return (default 20)"),
		),
	)
}

// fetchFilingTool returns the tool definition for fetch_filing.
func fetchFilingTool() mcp.Tool {
	return mcp.NewTool("fetch_filing",
		mcp.WithDescription("Download a specific filing document to the local cache. Use the document_id from list_filings or get_latest output. "+
			"Returns {document_id, local_path, content_type, file_size_bytes, source, is_archive?, total_in_archive?, truncated?}. "+
			"When is_archive is true the filing was delivered as a zip archive; call list_zip_contents with the same ch_number and document_id to see all extracted files. "+
			"When truncated is true the archive contained more files than could be extracted; total_in_archive shows the full count."),
		mcp.WithString("ch_number",
			mcp.Required(),
			mcp.Description("Companies House number of the company the filing belongs to"),
		),
		mcp.WithString("document_id",
			mcp.Required(),
			mcp.Description("document_id from list_filings or get_latest output"),
		),
	)
}

// getLatestTool returns the tool definition for get_latest.
func getLatestTool() mcp.Tool {
	return mcp.NewTool("get_latest",
		mcp.WithDescription("Fetch the most recent filing of a given category for a UK company. Combines list_filings and fetch_filing in a single call. "+
			"Returns the same fields as fetch_filing: {document_id, local_path, content_type, file_size_bytes, source, is_archive?, total_in_archive?, truncated?}. "+
			"When is_archive is true the filing was delivered as a zip archive; call list_zip_contents with the same ch_number and document_id to see all extracted files."),
		mcp.WithString("ch_number",
			mcp.Required(),
			mcp.Description("Companies House number"),
		),
		mcp.WithString("category",
			mcp.Required(),
			mcp.Description("Filing category to fetch. Common values: 'accounts', 'confirmation-statement'."),
		),
	)
}

// extractXBRLFactsTool returns the tool definition for extract_xbrl_facts.
func extractXBRLFactsTool() mcp.Tool {
	return mcp.NewTool("extract_xbrl_facts",
		mcp.WithDescription("Parse a cached iXBRL .xhtml file and return structured financial facts as JSON. "+
			"Use the local_path from fetch_filing or get_latest when content_type is application/xhtml+xml, or any .xhtml entry local_path from list_zip_contents. "+
			"Returns {facts, count, truncated, render_type, warnings?}. "+
			"render_type is \"native_ixbrl\" or \"pdf_rendered\"; when \"pdf_rendered\", the warnings array explains that narrative text is not reliably accessible and lists any alternative formats available in the archive. "+
			"When truncated is true the document contained more facts than the cap — use name_prefix to narrow the query and retrieve the facts you need."),
		mcp.WithString("local_path",
			mcp.Required(),
			mcp.Description("Absolute path to a cached iXBRL .xhtml file, as returned by fetch_filing or get_latest"),
		),
		mcp.WithString("name_prefix",
			mcp.Description("Optional concept name prefix filter. Only facts whose name starts with this value are returned (e.g. 'Revenue' returns all Revenue* facts). Case-sensitive. Applies to both numeric and text facts."),
		),
		mcp.WithBoolean("include_text_facts",
			mcp.Description("Include ix:nonNumeric text facts such as company name and director names. Default false — only numeric ix:nonFraction facts are returned. name_prefix filtering applies to text facts when this is true."),
		),
	)
}

// listZipContentsTool returns the tool definition for list_zip_contents.
func listZipContentsTool() mcp.Tool {
	return mcp.NewTool("list_zip_contents",
		mcp.WithDescription("List all documents extracted from a zip archive filing. "+
			"Use this when fetch_filing or get_latest returns is_archive: true; requires a prior fetch_filing or get_latest call. "+
			"Returns {entries: [{filename, local_path, content_type, file_size_bytes, is_primary}], total_in_archive?, truncated?}. "+
			"The primary document (is_primary true) is the one whose local_path was returned by fetch_filing or get_latest; other entries (e.g. a PDF alongside an iXBRL) can be read directly via their local_path. "+
			"Results are capped at 20 entries per archive; when truncated is true, total_in_archive shows the full count before the cap."),
		mcp.WithString("ch_number",
			mcp.Required(),
			mcp.Description("Companies House number of the company the filing belongs to"),
		),
		mcp.WithString("document_id",
			mcp.Required(),
			mcp.Description("document_id from list_filings or get_latest output — the same value used with fetch_filing"),
		),
	)
}

// getCompanyBriefTool returns the tool definition for get_company_brief.
func getCompanyBriefTool() mcp.Tool {
	return mcp.NewTool("get_company_brief",
		mcp.WithDescription("One-shot company research brief. Resolves a company by name or Companies House number, then returns its "+
			"profile, latest accounts, latest confirmation statement, active charges, active persons with significant control, "+
			"insolvency cases recorded by Companies House, and a bounded list of recent filings. Metadata and document_ids only — no document content is "+
			"fetched; follow up with fetch_filing for any document you need to read. If company_name matches more than one company, "+
			"returns an error listing candidates ({ch_number}: {name} ({status}, {type})) — retry with an explicit ch_number."),
		mcp.WithString("company_name",
			mcp.Description("Company name to search for. Required if ch_number is omitted. Ignored if ch_number is given."),
		),
		mcp.WithString("ch_number",
			mcp.Description("Companies House number, if already known. Skips the name search."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of recent_filings entries to return (default 20, max 40)"),
		),
	)
}

// clearCacheTool returns the tool definition for clear_cache.
func clearCacheTool() mcp.Tool {
	return mcp.NewTool("clear_cache",
		mcp.WithDescription("Delete downloaded filing documents from the local cache. Pass ch_number to clear one company; omit it to clear everything. Returns {deleted_files, freed_bytes, db_records_removed}. document_ids from list_filings remain valid after a clear."),
		mcp.WithString("ch_number",
			mcp.Description("Companies House number to scope the clear. Omit to clear all cached filings."),
		),
	)
}
