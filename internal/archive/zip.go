// Package archive extracts documents from zip archives served by the Companies House API.
package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strings"

	"github.com/riftwerx/company-research/internal/mime"
)

// MaxEntries is the maximum number of file entries extracted from a single archive.
// Entries beyond this cap are silently skipped.
const MaxEntries = 20

// ErrBodyTooLarge is returned by ReadBody when the body exceeds the size limit.
var ErrBodyTooLarge = errors.New("zip filing exceeds maximum size limit")

// Entry holds a single file extracted from a zip archive.
type Entry struct {
	// Filename is the base name of the file as stored in the archive.
	Filename string
	// Content is the raw file bytes.
	Content []byte
	// ContentType is the MIME type inferred from the file extension.
	ContentType string
	// IsPrimary is true for the entry selected as the primary document.
	IsPrimary bool
}

// safeInt64Size converts a uint64 byte count to int64 for use with io.LimitedReader.
// The callers pass safeInt64Size(n)+1 to LimitedReader so they can detect when the
// limit is exactly reached. To prevent that +1 from overflowing to math.MinInt64
// (which would make LimitedReader return EOF immediately and bypass the check), the
// return value is capped at math.MaxInt64-1, leaving room for the +1.
func safeInt64Size(n uint64) int64 {
	if n >= math.MaxInt64-1 {
		return math.MaxInt64 - 1
	}
	return int64(n)
}

// ReadBody reads at most maxBytes of zip data from r.
// Returns ErrBodyTooLarge if the body exceeds maxBytes, or a wrapped error on read failure.
func ReadBody(r io.Reader, maxBytes uint64) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: safeInt64Size(maxBytes) + 1}
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read zip body: %w", err)
	}
	if lr.N == 0 {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

// ExtractAll extracts all files from a zip archive. The primary document is the entry
// selected by tier (largest .xhtml → largest .html/.htm → largest .pdf → largest file)
// and has IsPrimary set to true; it is always first in the returned slice. Entries
// beyond MaxEntries are silently skipped.
//
// Two zip-bomb defences are applied:
//  1. Header check: the sum of all uncompressed entry sizes must not exceed maxBytes.
//  2. Per-entry read: each entry is read through a LimitedReader bounded by maxBytes.
//
// Returns an error if the archive is malformed, the total uncompressed size exceeds
// maxBytes, or the archive contains no files. totalFiles is the count of non-directory
// entries in the archive before the MaxEntries cap; len(entries) < totalFiles means
// the result was truncated.
func ExtractAll(zipData []byte, maxBytes uint64) ([]Entry, int, error) {
	r, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, 0, fmt.Errorf("open zip: %w", err)
	}

	// Zip bomb defence: check total uncompressed size before extracting anything.
	// Without this guard, adding f.UncompressedSize64 to totalUncompressed could
	// wrap around (uint64 overflow); rejecting before the addition keeps the
	// invariant totalUncompressed ≤ maxBytes, which also ensures the subtraction
	// maxBytes-totalUncompressed cannot underflow.
	var totalUncompressed uint64
	var totalFiles int
	for _, f := range r.File {
		if !f.FileInfo().IsDir() {
			totalFiles++
			if f.UncompressedSize64 > maxBytes-totalUncompressed {
				return nil, 0, fmt.Errorf("zip uncompressed content exceeds %d-byte limit", maxBytes)
			}
			totalUncompressed += f.UncompressedSize64
		}
	}

	// Priority tiers for primary document selection.
	const (
		tierXHTML = 0
		tierHTML  = 1
		tierPDF   = 2
		tierOther = 3
	)

	type candidate struct {
		f    *zip.File
		tier int
	}

	var best *candidate

	// First pass: identify the primary document.
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Name))
		var tier int
		switch ext {
		case ".xhtml":
			tier = tierXHTML
		case ".html", ".htm":
			tier = tierHTML
		case ".pdf":
			tier = tierPDF
		default:
			tier = tierOther
		}

		if best == nil ||
			tier < best.tier ||
			(tier == best.tier && f.UncompressedSize64 > best.f.UncompressedSize64) {
			best = &candidate{f: f, tier: tier}
		}
	}

	if best == nil {
		return nil, 0, fmt.Errorf("zip contains no files")
	}

	// Resolve a unique base name for every non-directory file entry. When two
	// entries share the same base name (e.g. "subdir1/report.xhtml" and
	// "subdir2/report.xhtml"), a numeric suffix is appended to all but the first:
	// stem_1.ext, stem_2.ext, … The first occurrence keeps its original name.
	nameForFile := make(map[*zip.File]string, totalFiles)
	{
		baseCount := make(map[string]int, totalFiles)
		for _, f := range r.File {
			if !f.FileInfo().IsDir() {
				baseCount[path.Base(f.Name)]++
			}
		}
		seq := make(map[string]int, totalFiles)
		for _, f := range r.File {
			if f.FileInfo().IsDir() {
				continue
			}
			base := path.Base(f.Name)
			if baseCount[base] == 1 {
				nameForFile[f] = base
			} else {
				seq[base]++
				n := seq[base]
				if n == 1 {
					// First occurrence of a duplicate keeps its original name.
					nameForFile[f] = base
				} else {
					// Subsequent occurrences get a numeric suffix: stem_2.ext, stem_3.ext, …
					ext := path.Ext(base)
					stem := strings.TrimSuffix(base, ext)
					nameForFile[f] = fmt.Sprintf("%s_%d%s", stem, n, ext)
				}
			}
		}
	}

	// Second pass: extract all file entries (up to MaxEntries), primary first.
	entries := make([]Entry, 0, len(r.File))
	count := 0

	// readEntry extracts a single zip file entry using the pre-resolved name.
	readEntry := func(f *zip.File, name string) (Entry, error) {
		if name == "." || name == ".." {
			return Entry{}, fmt.Errorf("zip entry %q has unusable base name", f.Name)
		}

		rc, openErr := f.Open()
		if openErr != nil {
			return Entry{}, fmt.Errorf("open zip entry %q: %w", f.Name, openErr)
		}
		defer rc.Close() //nolint:errcheck // deferred close on read-only entry

		// A second size guard: UncompressedSize64 in the zip header is written by the
		// archive creator and can be falsified (e.g. set to 0 while the entry actually
		// expands to gigabytes). Reading through a LimitedReader ensures extraction is
		// bounded even if the header-sum check above was defeated.
		//
		// Note: each entry gets its own full maxBytes budget rather than a shared
		// remaining budget. In the adversarial case (all headers falsified to 0),
		// up to MaxEntries×maxBytes could be allocated before a per-entry limit fires.
		// In practice the zip data itself is bounded to maxBytes by ReadBody, so the
		// compressed input constrains total expansion.
		lr := &io.LimitedReader{R: rc, N: safeInt64Size(maxBytes) + 1}
		data, readErr := io.ReadAll(lr)
		if readErr != nil {
			return Entry{}, fmt.Errorf("read zip entry %q: %w", f.Name, readErr)
		}
		if lr.N == 0 {
			return Entry{}, fmt.Errorf("zip entry %q exceeds %d-byte limit", f.Name, maxBytes)
		}

		ext := strings.ToLower(path.Ext(name))
		return Entry{
			Filename:    name,
			Content:     data,
			ContentType: mime.TypeFromExt(ext),
			IsPrimary:   f == best.f,
		}, nil
	}

	// Extract the primary document first.
	primary, err := readEntry(best.f, nameForFile[best.f])
	if err != nil {
		return nil, 0, err
	}
	entries = append(entries, primary)
	count++

	// Extract remaining non-primary entries, up to MaxEntries total.
	for _, f := range r.File {
		if count >= MaxEntries {
			break
		}
		if f.FileInfo().IsDir() || f == best.f {
			continue
		}
		entry, err := readEntry(f, nameForFile[f])
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, entry)
		count++
	}

	return entries, totalFiles, nil
}
