package cache

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errReader is a reader that immediately returns an error, used to test Put error paths.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// infiniteReader is a reader that never returns EOF, used to test the Put size limit.
type infiniteReader struct{}

func (infiniteReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// newTestCache creates a Cache backed by a temp directory.
func newTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := New(Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create test cache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestNewDefaultConfig(t *testing.T) {
	t.Parallel()

	// Act
	cfg := NewDefaultConfig()

	// Assert — zero value is the only contract: callers rely on New accepting it
	assert.Empty(t, cfg.BaseDir)
}

// TestNewDefaultBaseDir exercises the code path where BaseDir is empty and the
// cache falls back to os.UserCacheDir(). Not run in parallel because t.Setenv
// modifies a process-wide environment variable.
// XDG_CACHE_HOME controls os.UserCacheDir() on Linux; skipped on other platforms.
func TestNewDefaultBaseDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CACHE_HOME only controls os.UserCacheDir on Linux")
	}

	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	// Act
	c, err := New(Config{}) // empty BaseDir → resolves via XDG_CACHE_HOME

	// Assert
	require.NoError(t, err)
	if c != nil {
		defer c.Close()
	}
	assert.DirExists(t, filepath.Join(tmpDir, "company-research"))
}

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("should create the database file on first open", func(t *testing.T) {
		t.Parallel()

		// Arrange
		dir := t.TempDir()

		// Act
		c, err := New(Config{BaseDir: dir})

		// Assert
		require.NoError(t, err)
		assert.NotNil(t, c)
		defer c.Close()
		assert.FileExists(t, filepath.Join(dir, dbFileName))
	})

	t.Run("should return an error when the base directory cannot be created", func(t *testing.T) {
		t.Parallel()

		// Arrange: create a regular file, then try to use a path inside it as BaseDir.
		// os.MkdirAll cannot create a directory whose parent is a file.
		f, err := os.CreateTemp("", "not-a-dir-*")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		t.Cleanup(func() { os.Remove(f.Name()) })

		// Act
		_, err = New(Config{BaseDir: filepath.Join(f.Name(), "subdir")})

		// Assert
		assert.Error(t, err)
	})
}

func TestPut(t *testing.T) {
	t.Parallel()

	t.Run("should store file on disk and return its local path", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		content := []byte("PDF filing content")

		// Act
		localPath, written, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader(content))

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, len(content), written)
		assert.FileExists(t, localPath)
		assert.Equal(t, ".pdf", filepath.Ext(localPath))
		got, err := os.ReadFile(localPath)
		require.NoError(t, err)
		assert.Equal(t, content, got)
	})

	t.Run("should return an error when the filing exceeds the size limit", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", infiniteReader{})

		// Assert
		require.ErrorContains(t, err, "size limit")
		// Verify no temp files were left behind.
		matches, _ := filepath.Glob(filepath.Join(c.baseDir, cacheSubDir, "00445790", "abc123", "*.tmp"))
		assert.Empty(t, matches, "temp file should be cleaned up on size limit error")
	})

	t.Run("should return an error and clean up the temp file when the reader fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", errReader{})

		// Assert
		require.Error(t, err)
		// Verify no temp files were left behind.
		matches, _ := filepath.Glob(filepath.Join(c.baseDir, cacheSubDir, "00445790", "abc123", "*.tmp"))
		assert.Empty(t, matches, "temp file should be cleaned up on reader error")
	})

	t.Run("should return an error and remove the committed file when the DB insert fails", func(t *testing.T) {
		t.Parallel()

		// Arrange: close the DB so the INSERT will fail.
		c := newTestCache(t)
		require.NoError(t, c.Close())

		// Act
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data")))

		// Assert
		require.Error(t, err)
		assert.NoFileExists(t, filepath.Join(c.baseDir, cacheSubDir, "00445790", "abc123", "filing.pdf"))
	})

	t.Run("should use the provided filename when non-empty", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		content := []byte("iXBRL content")
		wantName := "213800Y5CJHXOATK7X11-2024-12-31-T01.xhtml"

		// Act
		localPath, _, err := c.Put(context.Background(), "03033634", "doc123", "application/xhtml+xml", wantName, bytes.NewReader(content))

		// Assert
		require.NoError(t, err)
		assert.FileExists(t, localPath)
		assert.Equal(t, wantName, filepath.Base(localPath))
	})

	t.Run("should overwrite an existing entry", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		first := []byte("first")
		second := []byte("second, longer content")

		// Act
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader(first))
		require.NoError(t, err)
		localPath, written, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader(second))

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, len(second), written)
		got, err := os.ReadFile(localPath)
		require.NoError(t, err)
		assert.Equal(t, second, got)
	})
}

func TestGet(t *testing.T) {
	t.Parallel()

	t.Run("should return entry for a stored document", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		content := []byte("filing data")
		storedPath, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader(content))
		require.NoError(t, err)

		// Act
		entry, err := c.Get(context.Background(), "00445790", "abc123")

		// Assert
		require.NoError(t, err)
		require.NotNil(t, entry)
		assert.Equal(t, storedPath, entry.LocalPath)
		assert.Equal(t, "application/pdf", entry.ContentType)
		assert.EqualValues(t, len(content), entry.FileSize)
	})

	t.Run("should return nil when document is not in database", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		entry, err := c.Get(context.Background(), "00445790", "notexist")

		// Assert
		require.NoError(t, err)
		assert.Nil(t, entry)
	})

	t.Run("should return nil when file has been deleted from disk", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		storedPath, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data")))
		require.NoError(t, err)
		os.Remove(storedPath)

		// Act
		entry, err := c.Get(context.Background(), "00445790", "abc123")

		// Assert
		require.NoError(t, err)
		assert.Nil(t, entry)
	})
}

func TestClear(t *testing.T) {
	t.Parallel()

	t.Run("should remove all files and DB rows when ch_number is empty", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		storedPath, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data1")))
		require.NoError(t, err)
		_, _, err = c.Put(context.Background(), "99999999", "def456", "application/pdf", "", bytes.NewReader([]byte("data2")))
		require.NoError(t, err)

		// Act
		result, err := c.Clear(context.Background(), "")

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, 2, result.DeletedFiles)
		assert.Positive(t, result.FreedBytes)
		assert.EqualValues(t, 2, result.DBRecords)
		assert.NoFileExists(t, storedPath)
	})

	t.Run("should remove only the specified company's files and rows", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data1")))
		require.NoError(t, err)
		keptPath, _, err := c.Put(context.Background(), "99999999", "def456", "application/pdf", "", bytes.NewReader([]byte("data2")))
		require.NoError(t, err)

		// Act
		result, err := c.Clear(context.Background(), "00445790")

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, 1, result.DeletedFiles)
		assert.EqualValues(t, 1, result.DBRecords)
		assert.FileExists(t, keptPath)
	})

	t.Run("should succeed when the cache is empty", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		result, err := c.Clear(context.Background(), "")

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, 0, result.DeletedFiles)
		assert.EqualValues(t, 0, result.FreedBytes)
		assert.EqualValues(t, 0, result.DBRecords)
	})

	t.Run("should return an error when the DB delete fails", func(t *testing.T) {
		t.Parallel()

		// Arrange: close the DB so the DELETE statement will fail.
		// os.RemoveAll still succeeds (file system is fine).
		c := newTestCache(t)
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data")))
		require.NoError(t, err)
		require.NoError(t, c.Close())

		// Act
		_, err = c.Clear(context.Background(), "")

		// Assert
		assert.Error(t, err)
	})
}

func TestPutZipEntries(t *testing.T) {
	t.Parallel()

	t.Run("should write all files to disk and return the primary path", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: true},
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("PDF"), IsPrimary: false},
		}

		// Act
		primaryPath, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, len(entries))

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "report.xhtml", filepath.Base(primaryPath))
		assert.FileExists(t, primaryPath)
		pdfPath := filepath.Join(filepath.Dir(primaryPath), "report.pdf")
		assert.FileExists(t, pdfPath)
	})

	t.Run("should index primary in filings for backward-compat Get", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: true},
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("PDF"), IsPrimary: false},
		}

		// Act
		_, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, len(entries))
		require.NoError(t, err)
		entry, err := c.Get(context.Background(), "03033634", "T01")

		// Assert
		require.NoError(t, err)
		require.NotNil(t, entry)
		assert.Equal(t, "application/xhtml+xml", entry.ContentType)
		assert.Equal(t, "report.xhtml", filepath.Base(entry.LocalPath))
	})

	t.Run("should index all entries in zip_entries and preserve total_in_archive", func(t *testing.T) {
		t.Parallel()

		// Arrange — archive has 5 files but only 2 are extracted (simulating truncation)
		const totalInArchive = 5
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: true},
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("PDF"), IsPrimary: false},
		}

		// Act
		_, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, totalInArchive)
		require.NoError(t, err)
		records, total, err := c.GetZipEntries(context.Background(), "03033634", "T01")

		// Assert
		require.NoError(t, err)
		require.Len(t, records, 2)
		assert.Equal(t, totalInArchive, total)
		assert.True(t, records[0].IsPrimary)
		assert.Equal(t, "report.xhtml", records[0].Filename)
		assert.False(t, records[1].IsPrimary)
		assert.Equal(t, "report.pdf", records[1].Filename)
		assert.Positive(t, records[1].FileSize)
		assert.FileExists(t, records[1].LocalPath)
	})

	t.Run("should overwrite entries on repeat call", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		first := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<v1/>"), IsPrimary: true},
		}
		second := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<v2/>"), IsPrimary: true},
		}

		// Act
		_, err := c.PutZipEntries(context.Background(), "03033634", "T01", first, len(first))
		require.NoError(t, err)
		primaryPath, err := c.PutZipEntries(context.Background(), "03033634", "T01", second, len(second))
		require.NoError(t, err)

		// Assert
		got, readErr := os.ReadFile(primaryPath)
		require.NoError(t, readErr)
		assert.Equal(t, "<v2/>", string(got))
	})

	t.Run("should return error when no entry is marked primary", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: false},
		}

		// Act
		_, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, len(entries))

		// Assert
		assert.ErrorContains(t, err, "primary")
	})
}

func TestGetZipEntries(t *testing.T) {
	t.Parallel()

	t.Run("should return nil for a non-zip filing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		_, _, err := c.Put(context.Background(), "00445790", "abc123", "application/pdf", "", bytes.NewReader([]byte("data")))
		require.NoError(t, err)

		// Act
		records, total, err := c.GetZipEntries(context.Background(), "00445790", "abc123")

		// Assert
		require.NoError(t, err)
		assert.Nil(t, records)
		assert.Zero(t, total)
	})

	t.Run("should return nil for an unknown document", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		records, total, err := c.GetZipEntries(context.Background(), "00445790", "notexist")

		// Assert
		require.NoError(t, err)
		assert.Nil(t, records)
		assert.Zero(t, total)
	})

	t.Run("should return nil after Clear removes the filing", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: true},
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("PDF"), IsPrimary: false},
		}
		_, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, len(entries))
		require.NoError(t, err)

		// Act
		_, err = c.Clear(context.Background(), "03033634")
		require.NoError(t, err)
		records, _, err := c.GetZipEntries(context.Background(), "03033634", "T01")

		// Assert
		require.NoError(t, err)
		assert.Nil(t, records)
	})
}

func TestClear_ZipEntries(t *testing.T) {
	t.Parallel()

	t.Run("should remove zip_entries records and files when clearing all", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		entries := []ZipCacheEntry{
			{Filename: "report.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<xhtml/>"), IsPrimary: true},
			{Filename: "report.pdf", ContentType: "application/pdf", Content: []byte("PDF"), IsPrimary: false},
		}
		primaryPath, err := c.PutZipEntries(context.Background(), "03033634", "T01", entries, len(entries))
		require.NoError(t, err)

		// Act
		result, err := c.Clear(context.Background(), "")

		// Assert
		require.NoError(t, err)
		assert.EqualValues(t, 2, result.DeletedFiles) // xhtml + pdf
		assert.EqualValues(t, 3, result.DBRecords)    // 1 filings row + 2 zip_entries rows
		assert.NoFileExists(t, primaryPath)
		records, _, err := c.GetZipEntries(context.Background(), "03033634", "T01")
		require.NoError(t, err)
		assert.Nil(t, records)
	})

	t.Run("should remove zip_entries for the specified company only", func(t *testing.T) {
		t.Parallel()

		// Arrange — two companies, each with a zip filing
		c := newTestCache(t)
		entriesA := []ZipCacheEntry{
			{Filename: "a.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<a/>"), IsPrimary: true},
		}
		entriesB := []ZipCacheEntry{
			{Filename: "b.xhtml", ContentType: "application/xhtml+xml", Content: []byte("<b/>"), IsPrimary: true},
		}
		_, err := c.PutZipEntries(context.Background(), "00000001", "T01", entriesA, len(entriesA))
		require.NoError(t, err)
		_, err = c.PutZipEntries(context.Background(), "00000002", "T02", entriesB, len(entriesB))
		require.NoError(t, err)

		// Act — clear only company A
		_, err = c.Clear(context.Background(), "00000001")
		require.NoError(t, err)

		// Assert — company A gone, company B intact
		recordsA, _, err := c.GetZipEntries(context.Background(), "00000001", "T01")
		require.NoError(t, err)
		assert.Nil(t, recordsA)

		recordsB, _, err := c.GetZipEntries(context.Background(), "00000002", "T02")
		require.NoError(t, err)
		assert.Len(t, recordsB, 1)
	})
}

func TestStoreFilingRef(t *testing.T) {
	t.Parallel()

	const (
		chNumber      = "00445790"
		transactionID = "MzI1MDk3NjkxOGFkaXF6a2N4"
		documentURL   = "https://document-api.company-information.service.gov.uk/document/abc123"
	)

	t.Run("should generate and store a new document_id", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		docID, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, docID)
		// UUID v4 format: 8-4-4-4-12 hex chars
		assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, docID)
	})

	t.Run("should be idempotent — same document_id on repeat calls", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		first, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)
		require.NoError(t, err)
		second, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)
		require.NoError(t, err)

		// Assert
		assert.Equal(t, first, second)
	})

	t.Run("should generate different document_ids for different filings", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		id1, err := c.StoreFilingRef(context.Background(), chNumber, "txn-1", "https://document-api.company-information.service.gov.uk/document/doc1")
		require.NoError(t, err)
		id2, err := c.StoreFilingRef(context.Background(), chNumber, "txn-2", "https://document-api.company-information.service.gov.uk/document/doc2")
		require.NoError(t, err)

		// Assert
		assert.NotEqual(t, id1, id2)
	})

	t.Run("should survive a cache Clear — refs are not deleted", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		docID, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)
		require.NoError(t, err)

		// Act
		_, clearErr := c.Clear(context.Background(), "")
		require.NoError(t, clearErr)

		// Assert — ref still resolvable after clear
		resolved, resolveErr := c.ResolveFilingRef(context.Background(), chNumber, docID)
		require.NoError(t, resolveErr)
		assert.Equal(t, documentURL, resolved)
	})
}

func TestResolveFilingRef(t *testing.T) {
	t.Parallel()

	const (
		chNumber      = "00445790"
		transactionID = "MzI1MDk3NjkxOGFkaXF6a2N4"
		documentURL   = "https://document-api.company-information.service.gov.uk/document/abc123"
	)

	t.Run("should return the document_url for a known document_id", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)
		docID, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)
		require.NoError(t, err)

		// Act
		resolved, err := c.ResolveFilingRef(context.Background(), chNumber, docID)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, documentURL, resolved)
	})

	t.Run("should return ErrFilingRefNotFound for an unknown document_id", func(t *testing.T) {
		t.Parallel()

		// Arrange
		c := newTestCache(t)

		// Act
		_, err := c.ResolveFilingRef(context.Background(), chNumber, "unknown-id")

		// Assert
		assert.ErrorIs(t, err, ErrFilingRefNotFound)
	})

	t.Run("should return ErrFilingRefNotFound when ch_number does not match", func(t *testing.T) {
		t.Parallel()

		// Arrange — store a ref under one company, try to resolve under another
		c := newTestCache(t)
		docID, err := c.StoreFilingRef(context.Background(), chNumber, transactionID, documentURL)
		require.NoError(t, err)

		// Act
		_, err = c.ResolveFilingRef(context.Background(), "99999999", docID)

		// Assert
		assert.ErrorIs(t, err, ErrFilingRefNotFound)
	})
}
