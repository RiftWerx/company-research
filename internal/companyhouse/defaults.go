package companyhouse

import "time"

// DefaultRate is the target request rate for the Companies House API.
// CH allows 600 requests per 5 minutes (2 req/sec absolute maximum).
// We target 80% of the cap (~1.5 req/sec = ~450 req/5min) to stay clear of the limit.
const DefaultRate = 1.5 // requests per second

// DefaultBurst is the token bucket burst size for CH API requests.
// CH's limit is a fixed 5-minute window (600 req, full reset at the boundary), not a
// smoothed rate — it does not penalise a short burst as long as the 5-minute total
// stays under the cap. 8 covers get_company_brief's worst case (one company profile +
// its accounts/confirmation-statement/charges/PSC/insolvency/recent-filings lookups)
// firing without the artificial per-request pacing delay an idle bucket would
// otherwise impose. Bursting only spends previously-accumulated idle tokens — it
// cannot raise the sustained rate above DefaultRate, so the 5-minute budget margin
// (~450 of 600 req at steady state) is unaffected.
const DefaultBurst = 8

// DefaultTimeout is the per-request HTTP timeout for CH API calls.
const DefaultTimeout = 10 * time.Second
