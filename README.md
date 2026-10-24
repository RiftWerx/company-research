# company-research

Fetches official company filings — annual reports, AGM documents, regulatory announcements — from
public sources and makes them available to an AI client. Works as an MCP server (for agents with
MCP support) or as a standalone CLI tool (for direct Bash invocations or scripting).

## Current Coverage

- UK companies via Companies House

## Installation

**Download a pre-built binary** (no Go required):

Download the latest release for your platform from
[GitHub Releases](https://github.com/riftwerx/company-research/releases/latest),
extract the archive, and place the binary on your `$PATH`.

**Install via `go install`:**

```bash
go install github.com/riftwerx/company-research/cmd/company-research@latest
```

**Or build from source** (installs to `$GOPATH/bin`):

```bash
git clone https://github.com/riftwerx/company-research
cd company-research
make local-release
```

> `make` targets require a Unix-like environment (Linux, macOS, or WSL on Windows).
> Windows users without WSL can run `go install ./cmd/company-research` directly —
> the binary is fully Windows-compatible; only the build tooling requires Unix.

The binary is named `company-research`. Ensure `$(go env GOPATH)/bin` is on your `$PATH`.

Verify the installation:

```bash
company-research --version
```

## Upgrading from v0.3.x

The binary was renamed from `company-research-mcp` to `company-research` after v0.3.0. After installing the new binary, remove the old one and update your MCP client config:

**`go install` users:**

```bash
rm "$(go env GOPATH)/bin/company-research-mcp"
```

**Pre-built binary users:** delete the old `company-research-mcp` binary from wherever you placed it on your `$PATH`.

Then update the `command:` field in your MCP client config from `company-research-mcp` to `company-research`.

**Migrate your cache** (optional — skipping means cached filings are re-downloaded on next use):

| Platform | Command |
|----------|---------|
| Linux | `mv ~/.cache/company-research.mcp ~/.cache/company-research` |
| macOS | `mv ~/Library/Caches/company-research.mcp ~/Library/Caches/company-research` |
| Windows | `Rename-Item "$env:LOCALAPPDATA\company-research.mcp" company-research` |

---

## Prerequisites

Register for a free Companies House API key at
https://developer.company-information.service.gov.uk

## Client Configuration

### Claude Code

```bash
claude mcp add --transport stdio company-research \
  --scope user \
  --env CH_API_KEY=your-key-here \
  -- company-research
```

### Claude Desktop

Add to `claude_desktop_config.json`:

- **macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows:** `%APPDATA%\Claude\claude_desktop_config.json`

```json
{
  "mcpServers": {
    "company-research": {
      "command": "company-research",
      "env": {
        "CH_API_KEY": "your-key-here"
      }
    }
  }
}
```

### Cursor

Add to `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (project):

```json
{
  "mcpServers": {
    "company-research": {
      "command": "company-research",
      "env": {
        "CH_API_KEY": "your-key-here"
      }
    }
  }
}
```

### Windsurf

Add to `~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "company-research": {
      "command": "company-research",
      "env": {
        "CH_API_KEY": "your-key-here"
      }
    }
  }
}
```

### Continue.dev

Add the server under `mcpServers` in your Continue `config.yaml` (global path:
`~/.continue/config.yaml`). MCP servers are available in Agent mode.

```yaml
mcpServers:
  - name: company-research
    command: company-research
    env:
      CH_API_KEY: your-key-here
```

## CLI Mode

The binary can also be used directly from the shell or from an agent's Bash tool — useful for
scripting or for agents that prefer direct invocations over MCP tool calls.

```bash
export CH_API_KEY=your-key-here

company-research search-company "Tesco"
company-research get-latest 00445790 accounts
company-research get-company-profile 00445790 | jq .name
```

Data commands output JSON to stdout; errors go to stderr with exit 1. The `install-skill`
setup command instead prints the installed file paths. Run `company-research --help` for a
full list of subcommands and flags.

## Agent Skills

Install embedded skill files to give your AI agent workflow knowledge for using
company-research effectively. No `CH_API_KEY` required.

### Claude Code

```bash
mkdir -p ~/.claude/skills/
company-research install-skill ~/.claude/skills/
```

Installs both the MCP skill and the CLI skill. To install only one:

```bash
company-research install-skill --mcp ~/.claude/skills/   # MCP mode only
company-research install-skill --cli ~/.claude/skills/   # CLI mode only
```

### Other agents

Check your agent's documentation for its skill or custom-prompt directory, then:

```bash
company-research install-skill <agent-skill-directory>
```

---

## Available Tools

| Tool | Description |
|---|---|
| `search_company` | Search for UK companies by name |
| `get_company_profile` | Get a company profile by Companies House number |
| `list_filings` | List filing history for a company; returns a `document_id` for each filing (filings without a downloadable document are excluded) |
| `fetch_filing` | Download a specific filing document using the `document_id` from `list_filings` |
| `get_latest` | Fetch the most recent filing of a given category; returns a `document_id` alongside the local path |
| `get_company_brief` | One-shot research brief: resolves a company by name or number, then returns profile + latest accounts + latest confirmation statement + active charges + active persons with significant control + insolvency cases recorded by Companies House + recent filings, all as metadata and `document_id`s only |
| `list_zip_contents` | List all documents extracted from a zip archive filing; use when `fetch_filing` or `get_latest` returns `is_archive: true` |
| `extract_xbrl_facts` | Parse a cached iXBRL `.xhtml` file and return structured financial facts as JSON; also reports whether the document is native iXBRL or PDF-rendered |
| `clear_cache` | Delete cached filing documents |

### Document formats

Filings are returned as **PDF** or **iXBRL** (`.xhtml`) depending on what Companies House provides.

When a filing is served as a zip archive, all documents are extracted and the primary document is
selected automatically. The `fetch_filing` / `get_latest` response will include `is_archive: true`
alongside `local_path` and `document_id` for the primary file. Call `list_zip_contents` with the
same `ch_number` and `document_id` to get the full manifest — each entry has its own `local_path`,
`content_type`, and `is_primary` flag, so secondary documents (e.g. a PDF companion alongside an
iXBRL) can be accessed directly. When an archive contains more than 20 files, `truncated: true` is
set and `total_in_archive` shows the full count.

### Extracting financial data from iXBRL filings

When `fetch_filing` or `get_latest` returns `content_type: application/xhtml+xml`, the file is an
iXBRL document containing tagged financial data. Use `extract_xbrl_facts` to parse it:

```
extract_xbrl_facts(local_path=<path from fetch_filing>)
```

Returns `{facts, count, truncated, render_type, warnings?}`. Each fact has `name`, `value`,
`period`, and `unit`. When `truncated` is `true` the document contained more than 2,000 facts —
use `name_prefix` to narrow the query (e.g. `name_prefix="Revenue"` returns only Revenue-related
facts).

By default only numeric facts (`ix:nonFraction`) are returned. Pass `include_text_facts=true` to
also include text facts (`ix:nonNumeric`) such as company name and director names; `name_prefix`
filtering applies to text facts too when this is set.

`render_type` is `"native_ixbrl"` for standard filings or `"pdf_rendered"` for filings produced
by a PDF-to-HTML converter (e.g. `pdf2htmlEX`). PDF-rendered filings have fully extractable XBRL
facts but fragmented narrative text (MD&A, notes); when detected a `warnings` array explains this
and the document will not be readable as prose. If the filing came from a zip archive, the warning
also names any alternative formats available (e.g. a PDF) and directs you to use `list_zip_contents`
to access them.

### Local cache

Downloaded filings are stored on disk and reused on subsequent requests:

| Platform | Path |
|---|---|
| Linux (default) | `~/.cache/company-research/` |
| macOS (default) | `~/Library/Caches/company-research/` |
| Windows (default) | `%LOCALAPPDATA%\company-research\` |

Use the `clear_cache` tool to free disk space.

## Examples

(See [EXAMPLE_PROMPTS.md](EXAMPLE_PROMPTS.md) for more usage examples)

### Company status check

> Find Monzo Bank and tell me whether it's still active

*Tools: `search_company` → `get_company_profile`*

**Agent:** Monzo Bank Limited (CH: 09446231) is **active**. It's a private limited company
registered in England & Wales. Registered office: Broadwalk House, 5 Appold Street, London,
EC2A 2AG. SIC code: 64191 (Banks).

---

### Fetch and summarise historical accounts

> Look up Rolls-Royce Holdings, then download and summarise the annual accounts for the year ended 31 December 2023

*Tools: `search_company` → `get_company_profile` → `list_filings` → `fetch_filing`*

**Historical illustrative response (FY2023):** Found Rolls-Royce Holdings plc (CH: 07524813),
active. Fetching its accounts for the year ended 31 December 2023… downloaded a 4.2 MB PDF to
the local cache.

*[reads document]*

The accounts cover the year ended 31 December 2023. Revenue was £16.5 billion (up 17%
year-on-year), driven by growth in Civil Aerospace. Underlying operating profit was £1.6
billion, compared to £652 million in 2022. Net debt reduced from £3.3 billion to £0.2
billion. The auditors issued an unqualified opinion.

---

### One-shot research brief

> Give me a quick research brief on Tesco — accounts, ownership, any charges or insolvency

*Tools: `get_company_brief`*

**Agent:** Tesco PLC (CH: 00445790) is **active**, a public limited company incorporated in
1947. The brief includes document IDs and filing dates for the latest accounts and
confirmation statement, plus 2 outstanding charges delivered in 2009. No active
persons with significant control or insolvency cases are recorded in the returned
registers. Fetch the accounts document to read its reporting period and financial
details; the brief does not report filing deadlines or reasons for PSC exemptions.

---

### Compare multiple companies by name

> Search for "British Steel" and compare the registered addresses of the top results

*Tools: `search_company` → `get_company_profile` ×N*

**Agent:**

| Company | CH Number | Status | Registered Address |
|---|---|---|---|
| British Steel Limited | 09523808 | Active | Brigg Road, Scunthorpe, DN16 1BP |
| British Steel Pension Scheme Trustee Ltd | 06437005 | Active | 1 Kingsway, London, WC2B 6AN |
| British Steel (Overseas Holdings) Ltd | 04669999 | Dissolved | — |
