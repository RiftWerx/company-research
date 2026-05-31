// Package cache implements local storage for downloaded company filings.
// It owns the cache directory layout, file I/O, and the SQLite filing index.
package cache

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for database/sql

	"github.com/riftwerx/company-research/internal/mime"
)

// cacheSubDir is the path under BaseDir where filing files are stored.
const cacheSubDir = "cache/uk"

// dbFileName is the name of the SQLite database file within BaseDir.
const dbFileName = "index.db"

// MaxFileSizeBytes is the maximum permitted size for a single cached filing document.
// Responses larger than this limit are rejected to prevent unbounded disk usage.
// The zip extraction layer uses this same limit to bound network reads and uncompressed content.
const MaxFileSizeBytes uint64 = 200 * 1024 * 1024 // 200 MiB

// FilingEntry holds the metadata for a cached filing document.
// Cache.Get returns nil when the filing is not cached.
type FilingEntry struct {
	LocalPath   string
	ContentType string
	FileSize    int64
}

// ClearResult holds the statistics from a Clear operation.
type ClearResult struct {
	DeletedFiles int64
	FreedBytes   int64
	DBRecords    int64
}

// ZipCacheEntry is a single document to be written as part of a zip-derived filing.
type ZipCacheEntry struct {
	Filename    string
	ContentType string
	Content     []byte
	IsPrimary   bool
}

// ZipEntryRecord is a single row returned by GetZipEntries.
type ZipEntryRecord struct {
	Filename    string
	LocalPath   string
	ContentType string
	FileSize    int64
	IsPrimary   bool
}

// NewDefaultConfig returns the default Cache configuration.
func NewDefaultConfig() Config {
	return Config{}
}

// Config holds the configuration for the Cache.
// The zero value is valid; empty fields are replaced with defaults.
type Config struct {
	// BaseDir is the root directory for cached files and the index database.
	// Default: {os.UserCacheDir()}/company-research
	// (e.g. ~/.cache/company-research on Linux,
	//  ~/Library/Caches/company-research on macOS,
	//  %LOCALAPPDATA%\company-research on Windows)
	BaseDir string
}

// Cache stores downloaded filings on disk and indexes them in SQLite.
// Construct with New; the zero value is not usable.
type Cache struct {
	db      *sql.DB
	root    *os.Root
	baseDir string
}

// New opens or creates the cache at the configured BaseDir.
// The SQLite database and cache directories are created if they do not exist.
func New(cfg Config) (*Cache, error) {
	baseDir := cfg.BaseDir
	if baseDir == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("resolve cache dir: %w", err)
		}
		baseDir = filepath.Join(cacheDir, "company-research")
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return nil, fmt.Errorf("open cache root: %w", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(baseDir, dbFileName))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}

	// WAL mode improves write performance and allows readers and writers to proceed
	// concurrently without blocking each other. This is not required for stdio MCP
	// transport (which is single-threaded), but makes the cache safer if the server
	// is ever extended to support an HTTP/SSE transport with concurrent tool calls.
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		_ = root.Close()
		_ = db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if err := migrate(context.Background(), db); err != nil {
		_ = root.Close()
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return &Cache{db: db, root: root, baseDir: baseDir}, nil
}

// ErrOutsideCache is returned by ValidatePath when the resolved path is not
// within the cache file subtree.
var ErrOutsideCache = errors.New("path is outside the cache directory")

// ErrFilingRefNotFound is returned by ResolveFilingRef when the given document_id
// is not present in the filing_refs table.
var ErrFilingRefNotFound = errors.New("document_id not found")

// ValidatePath resolves symlinks in path and verifies it is within the cache
// file subtree (baseDir/cache/uk/...). It returns the resolved real path on
// success. If the path does not exist or cannot be resolved, the underlying OS
// error is returned. If the resolved path is outside the cache file subtree,
// ErrOutsideCache is returned.
func (c *Cache) ValidatePath(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	fileRoot := filepath.Join(c.baseDir, cacheSubDir) + string(filepath.Separator)
	if !strings.HasPrefix(real, fileRoot) {
		return "", ErrOutsideCache
	}
	return real, nil
}

// ParseFilingPath extracts the ch_number and doc_id from an absolute real path
// that has already been validated by ValidatePath (i.e. confirmed within the
// cache file subtree). Returns an error if the path structure is unexpected.
func (c *Cache) ParseFilingPath(realPath string) (chNumber, docID string, err error) {
	cacheRoot := filepath.Join(c.baseDir, cacheSubDir)
	rel, relErr := filepath.Rel(cacheRoot, realPath)
	if relErr != nil {
		return "", "", relErr
	}
	parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("unexpected cache path structure: %s", rel)
	}
	return parts[0], parts[1], nil
}

// Put stores body as a local file and records the filing in the index.
// If an entry for (chNumber, docID) already exists it is overwritten.
// Returns the local file path and the number of bytes written.
//
// filename is the desired file name (stem + extension, e.g. "report-2024.xhtml").
// If empty, it falls back to "filing"+fileExt(contentType).
//
// The write is atomic: body is written to a temp file in the same directory
// and renamed over the final path only after a successful close. This prevents
// a partially-written file from being visible as a valid cache entry.
func (c *Cache) Put(ctx context.Context, chNumber, docID, contentType, filename string, body io.Reader) (localPath string, written int64, err error) {
	relDir := filepath.Join(cacheSubDir, chNumber, docID)
	if err := c.root.MkdirAll(relDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create cache dir: %w", err)
	}

	name := filename
	if name == "" {
		name = "filing" + mime.Ext(contentType)
	}
	relFinal := filepath.Join(relDir, name)
	finalPath := filepath.Join(c.baseDir, relFinal) // absolute path stored in the DB and returned to callers

	// Write to a temp file in the same directory so Rename is atomic (same fs).
	relTmp := filepath.Join(relDir, fmt.Sprintf("filing-%016x.tmp", rand.Uint64())) //nolint:gosec // G404: temp file suffix does not require cryptographic randomness
	tmp, err := c.root.OpenFile(relTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}

	// Allow one extra byte so lr.N == 0 unambiguously signals the body exceeded the limit.
	lr := &io.LimitedReader{R: body, N: int64(MaxFileSizeBytes) + 1}
	written, copyErr := io.Copy(tmp, lr)
	closeErr := tmp.Close() // close before Rename — required on Windows
	switch {
	case copyErr != nil:
		_ = c.root.Remove(relTmp)
		return "", 0, fmt.Errorf("write file: %w", copyErr)
	case closeErr != nil:
		_ = c.root.Remove(relTmp)
		return "", 0, fmt.Errorf("close temp file: %w", closeErr)
	case lr.N == 0:
		// LimitedReader exhausted — body is at least MaxFileSizeBytes+1 bytes.
		_ = c.root.Remove(relTmp)
		return "", 0, fmt.Errorf("filing exceeds %d-byte size limit", MaxFileSizeBytes)
	}

	if err := c.root.Rename(relTmp, relFinal); err != nil {
		_ = c.root.Remove(relTmp)
		return "", 0, fmt.Errorf("commit file: %w", err)
	}

	_, err = c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO filings (ch_number, doc_id, local_path, content_type, file_size)
		 VALUES (?, ?, ?, ?, ?)`,
		chNumber, docID, finalPath, contentType, written,
	)
	if err != nil {
		_ = c.root.Remove(relFinal)
		return "", 0, fmt.Errorf("index filing: %w", err)
	}

	return finalPath, written, nil
}

// Get looks up a previously cached filing by company number and document ID.
// Returns nil if the filing is not indexed or the local file has been deleted.
func (c *Cache) Get(ctx context.Context, chNumber, docID string) (*FilingEntry, error) {
	var entry FilingEntry
	row := c.db.QueryRowContext(ctx,
		`SELECT local_path, content_type, file_size FROM filings WHERE ch_number = ? AND doc_id = ?`,
		chNumber, docID,
	)
	scanErr := row.Scan(&entry.LocalPath, &entry.ContentType, &entry.FileSize)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return nil, nil //nolint:nilerr // sql.ErrNoRows is not a failure; nil signals the cache miss
	}
	if scanErr != nil {
		return nil, fmt.Errorf("query filing: %w", scanErr)
	}

	// Convert the stored absolute path to a root-relative path for the existence check.
	// entry.LocalPath is always written by Put as filepath.Join(c.baseDir, ...) so Rel succeeds.
	relPath, relErr := filepath.Rel(c.baseDir, entry.LocalPath)
	if relErr != nil || strings.HasPrefix(relPath, "..") {
		return nil, nil //nolint:nilerr // path outside cache root — treat as miss
	}
	if _, statErr := c.root.Stat(relPath); statErr != nil {
		return nil, nil //nolint:nilerr // file missing from disk is a cache miss, not a failure
	}

	return &entry, nil
}

// PutZipEntries stores all documents extracted from a zip archive and indexes them.
// Each entry is written atomically using the same temp-file rename pattern as Put.
// All entries are indexed in zip_entries; the primary entry is also indexed in filings
// so that existing Get calls continue to return the primary document.
// totalInArchive is the count of non-directory entries in the source archive before any
// MaxEntries cap; it is stored so callers can detect whether the result was truncated.
// Returns the local file path of the primary document.
//
// All DB writes happen inside a single transaction. On any failure the transaction is
// rolled back and every file written during this call is removed, keeping the DB and
// disk in a consistent state.
func (c *Cache) PutZipEntries(ctx context.Context, chNumber, docID string, entries []ZipCacheEntry, totalInArchive int) (string, error) {
	hasPrimary := false
	for _, e := range entries {
		if e.IsPrimary {
			hasPrimary = true
			break
		}
	}
	if !hasPrimary {
		return "", fmt.Errorf("no primary entry in zip entries")
	}

	relDir := filepath.Join(cacheSubDir, chNumber, docID)
	if err := c.root.MkdirAll(relDir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}

	// writtenRels tracks relative paths of files committed to disk in this call.
	// The defer rolls back the transaction and removes those files on any failure,
	// keeping the DB and disk in sync.
	var writtenRels []string
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			for _, rel := range writtenRels {
				_ = c.root.Remove(rel)
			}
		}
	}()

	var primaryPath string

	for _, entry := range entries {
		name := entry.Filename
		if name == "" {
			name = "filing" + mime.Ext(entry.ContentType)
		}
		relFinal := filepath.Join(relDir, name)
		finalPath := filepath.Join(c.baseDir, relFinal)

		relTmp := filepath.Join(relDir, fmt.Sprintf("filing-%016x.tmp", rand.Uint64())) //nolint:gosec // G404: temp file suffix does not require cryptographic randomness
		tmp, err := c.root.OpenFile(relTmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", fmt.Errorf("create temp file: %w", err)
		}

		// Defence-in-depth: content was already bounded in archive.ExtractAll, but mirror the Put pattern.
		lr := &io.LimitedReader{R: bytes.NewReader(entry.Content), N: int64(MaxFileSizeBytes) + 1}
		written, copyErr := io.Copy(tmp, lr)
		closeErr := tmp.Close()
		switch {
		case copyErr != nil:
			_ = c.root.Remove(relTmp)
			return "", fmt.Errorf("write file: %w", copyErr)
		case closeErr != nil:
			_ = c.root.Remove(relTmp)
			return "", fmt.Errorf("close temp file: %w", closeErr)
		case lr.N == 0:
			_ = c.root.Remove(relTmp)
			return "", fmt.Errorf("filing exceeds %d-byte size limit", MaxFileSizeBytes)
		}

		if err := c.root.Rename(relTmp, relFinal); err != nil {
			_ = c.root.Remove(relTmp)
			return "", fmt.Errorf("commit file: %w", err)
		}
		writtenRels = append(writtenRels, relFinal)

		isPrimary := 0
		if entry.IsPrimary {
			isPrimary = 1
		}

		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO zip_entries (ch_number, doc_id, filename, local_path, content_type, file_size, is_primary, total_in_archive)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			chNumber, docID, name, finalPath, entry.ContentType, written, isPrimary, totalInArchive,
		)
		if err != nil {
			return "", fmt.Errorf("index zip entry: %w", err)
		}

		if entry.IsPrimary {
			_, err = tx.ExecContext(ctx,
				`INSERT OR REPLACE INTO filings (ch_number, doc_id, local_path, content_type, file_size)
				 VALUES (?, ?, ?, ?, ?)`,
				chNumber, docID, finalPath, entry.ContentType, written,
			)
			if err != nil {
				return "", fmt.Errorf("index primary filing: %w", err)
			}
			primaryPath = finalPath
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return primaryPath, nil
}

// GetZipEntries returns all documents indexed for the given filing and the total
// number of non-directory files in the source archive before any MaxEntries cap.
// Returns nil, 0, nil if no zip_entries rows exist for (chNumber, docID).
// len(records) < totalInArchive means the archive was truncated at extraction time.
func (c *Cache) GetZipEntries(ctx context.Context, chNumber, docID string) ([]ZipEntryRecord, int, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT filename, local_path, content_type, file_size, is_primary, total_in_archive
		 FROM zip_entries WHERE ch_number = ? AND doc_id = ?
		 ORDER BY is_primary DESC, filename`,
		chNumber, docID,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("query zip entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err() checked below

	var records []ZipEntryRecord
	var totalInArchive int
	for rows.Next() {
		var r ZipEntryRecord
		var isPrimary int
		if err := rows.Scan(&r.Filename, &r.LocalPath, &r.ContentType, &r.FileSize, &isPrimary, &totalInArchive); err != nil {
			return nil, 0, fmt.Errorf("scan zip entry: %w", err)
		}
		r.IsPrimary = isPrimary != 0
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate zip entries: %w", err)
	}
	return records, totalInArchive, nil
}

// Clear removes cached filings from disk and deletes their index records.
// If chNumber is non-empty only that company's filings are removed; otherwise all filings are removed.
//
// Both tables (filings and zip_entries) are deleted inside a single transaction so
// the DB is never left in a half-cleared state. The index is updated before files
// are removed so that a failed file removal leaves the cache in a consistent state
// (index and disk match).
func (c *Cache) Clear(ctx context.Context, chNumber string) (ClearResult, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return ClearResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // superseded by Commit; rollback error is irrelevant on success

	var dbRecords int64

	var sqlResult sql.Result
	if chNumber != "" {
		sqlResult, err = tx.ExecContext(ctx, `DELETE FROM filings WHERE ch_number = ?`, chNumber)
	} else {
		sqlResult, err = tx.ExecContext(ctx, `DELETE FROM filings`)
	}
	if err != nil {
		return ClearResult{}, fmt.Errorf("delete index records: %w", err)
	}
	n, err := sqlResult.RowsAffected()
	if err != nil {
		return ClearResult{}, fmt.Errorf("rows affected: %w", err)
	}
	dbRecords += n

	// Also clear zip_entries; the file-level RemoveAll below handles the actual files.
	var zipResult sql.Result
	if chNumber != "" {
		zipResult, err = tx.ExecContext(ctx, `DELETE FROM zip_entries WHERE ch_number = ?`, chNumber)
	} else {
		zipResult, err = tx.ExecContext(ctx, `DELETE FROM zip_entries`)
	}
	if err != nil {
		return ClearResult{}, fmt.Errorf("delete zip entry records: %w", err)
	}
	n, err = zipResult.RowsAffected()
	if err != nil {
		return ClearResult{}, fmt.Errorf("zip rows affected: %w", err)
	}
	dbRecords += n

	if err := tx.Commit(); err != nil {
		return ClearResult{}, fmt.Errorf("commit: %w", err)
	}

	targetDir := cacheSubDir
	if chNumber != "" {
		targetDir = filepath.Join(cacheSubDir, chNumber)
	}

	var result ClearResult
	result.DBRecords = dbRecords

	// Count files and bytes before removal, treating a missing directory as empty.
	_ = fs.WalkDir(c.root.FS(), targetDir, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // non-existent or inaccessible entry — treat as empty
		}
		if !d.IsDir() {
			if info, infoErr := d.Info(); infoErr == nil {
				result.DeletedFiles++
				result.FreedBytes += info.Size()
			}
		}
		return nil
	})

	if err := c.root.RemoveAll(targetDir); err != nil {
		return result, fmt.Errorf("remove cache dir: %w", err)
	}

	return result, nil
}

// StoreFilingRef returns the document_id for (chNumber, transactionID), generating
// and persisting a new UUID v4 if not already stored. Idempotent: concurrent or
// repeated calls for the same (chNumber, transactionID) always return the same document_id.
func (c *Cache) StoreFilingRef(ctx context.Context, chNumber, transactionID, documentURL string) (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("generate document id: %w", err)
	}
	// INSERT OR IGNORE: no-op if (ch_number, transaction_id) already exists.
	if _, err := c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO filing_refs (document_id, ch_number, transaction_id, document_url) VALUES (?, ?, ?, ?)`,
		id.String(), chNumber, transactionID, documentURL,
	); err != nil {
		return "", fmt.Errorf("insert filing ref: %w", err)
	}
	// Always read back the winner — handles both the fresh-insert and the pre-existing cases.
	var docID string
	if err := c.db.QueryRowContext(ctx,
		`SELECT document_id FROM filing_refs WHERE ch_number = ? AND transaction_id = ?`,
		chNumber, transactionID,
	).Scan(&docID); err != nil {
		return "", fmt.Errorf("query filing ref: %w", err)
	}
	return docID, nil
}

// ResolveFilingRef returns the document_url for the given (chNumber, documentID) pair.
// Returns ErrFilingRefNotFound if the document_id is not present in the filing_refs table.
func (c *Cache) ResolveFilingRef(ctx context.Context, chNumber, documentID string) (string, error) {
	var url string
	row := c.db.QueryRowContext(ctx,
		`SELECT document_url FROM filing_refs WHERE ch_number = ? AND document_id = ?`,
		chNumber, documentID,
	)
	if err := row.Scan(&url); errors.Is(err, sql.ErrNoRows) {
		return "", ErrFilingRefNotFound
	} else if err != nil {
		return "", fmt.Errorf("query filing ref: %w", err)
	}
	return url, nil
}

// Close closes the underlying root and database connection.
func (c *Cache) Close() error {
	return errors.Join(c.root.Close(), c.db.Close())
}
