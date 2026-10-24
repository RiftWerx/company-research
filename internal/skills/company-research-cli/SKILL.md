---
description: Workflow guide for using company-research as a CLI tool via Bash
when_to_use: >
  Use when researching UK companies, fetching Companies House filings, working
  with annual reports, confirmation statements, or XBRL financial data.
---

# company-research CLI workflow

## Setup

`CH_API_KEY` must be set in the environment — not accepted as a CLI flag.

Data commands output JSON to stdout; errors go to stderr with exit 1. `install-skill` instead
prints installed file paths. Use `jq` to extract fields from data-command output.

## Command reference

```
search-company <query> [-l/--limit N]
get-company-profile <ch-number>
list-filings <ch-number> [-c/--category STR] [--start N] [-l/--limit N]
fetch-filing <ch-number> <document-id>
get-latest <ch-number> <category>
get-company-brief [--company-name STR | --ch-number STR] [-l/--limit N]
list-zip-contents <ch-number> <document-id>
extract-xbrl-facts <local-path> [--name-prefix STR] [--include-text-facts]
clear-cache [--ch-number STR]
```

## Canonical sequence

```bash
company-research search-company "Tesco"                     # pick ch_number
company-research list-filings 00445790 --category accounts  # pick document_id
company-research fetch-filing 00445790 <document_id>        # get local_path
company-research extract-xbrl-facts <local_path>            # financial facts
```

`get-latest` shortcut — replaces `list-filings` + `fetch-filing`:
```bash
company-research get-latest 00445790 accounts
```

`get-company-brief` shortcut — for "research this company" requests, prefer this as the
first call: resolves by name or `--ch-number` and returns profile + latest accounts +
latest confirmation statement + active charges + active PSC + insolvency cases recorded
by Companies House + recent filings in one call:
```bash
company-research get-company-brief --company-name "Tesco"
```

## Key facts

- CH numbers are zero-padded to 8 digits: `00445790` not `445790`
- `list-filings` common categories: `accounts`, `confirmation-statement`; omit `--category` to discover all
- `document_id` is an opaque UUID from `list-filings`/`get-latest` output — never construct it
- `extract-xbrl-facts` takes `local_path` (from `fetch-filing`/`get-latest` output), **not** `document_id`

## ZIP archives

When `fetch-filing`/`get-latest` returns `"is_archive": true`:
```bash
company-research list-zip-contents 00445790 <document_id>  # pick entry: is_primary or content_type
company-research extract-xbrl-facts <entry_local_path>
```

## PDF-rendered iXBRL

When `extract-xbrl-facts` returns `"render_type": "pdf_rendered"`: XBRL facts are present but narrative text is inaccessible. Check `warnings` for alternative formats.
