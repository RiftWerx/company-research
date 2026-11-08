---
description: Workflow guide for using company-research as an MCP server
when_to_use: >
  Use when researching UK companies, fetching Companies House filings, working
  with annual reports, confirmation statements, or XBRL financial data.
---

# company-research MCP workflow

## Canonical sequence

Call in order: `search_company` → `list_filings` → `fetch_filing` → `extract_xbrl_facts`

`get_latest` shortcut: replaces `list_filings` + `fetch_filing` in one call (most recent filing in a category).

`get_company_brief` shortcut: for "research this company" requests, prefer this as the first call — it resolves by name or `ch_number` and returns profile + latest accounts + latest confirmation statement + active charges + active PSC + insolvency cases recorded by Companies House + recent filings in one call.

## Key facts

- CH numbers are zero-padded to 8 digits: `00445790` not `445790`
- `list_filings` common categories: `accounts`, `confirmation-statement`; omit `category` to discover all
- `document_id` is an opaque UUID minted by `list_filings`/`get_latest` — never construct it
- `extract_xbrl_facts` takes `local_path` (from `fetch_filing`/`get_latest` output), **not** `document_id`

## ZIP archives

When `fetch_filing`/`get_latest` returns `is_archive: true`:
1. Call `list_zip_contents` (same `ch_number` + `document_id`)
2. Choose an entry by `is_primary: true` or target `content_type`
3. Pass its `local_path` to `extract_xbrl_facts`

## PDF-rendered iXBRL

When `render_type` is `"pdf_rendered"`: XBRL facts are present but narrative text is inaccessible. Check `warnings` for alternative formats.
