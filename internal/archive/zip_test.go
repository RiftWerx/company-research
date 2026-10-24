package archive_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/riftwerx/company-research/internal/archive"
)

// buildZip creates an in-memory zip archive from a slice of [name, content] pairs.
func buildZip(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := w.Create(f[0])
		require.NoError(t, err, "buildZip: create %q", f[0])
		_, err = fw.Write([]byte(f[1]))
		require.NoError(t, err, "buildZip: write %q", f[0])
	}
	require.NoError(t, w.Close(), "buildZip: close")
	return buf.Bytes()
}

// errReader is a reader that immediately returns an error.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestExtractAll(t *testing.T) {
	t.Parallel()

	const limit = 1024 * 1024 // 1 MiB

	t.Run("primary selection", func(t *testing.T) {
		t.Parallel()

		t.Run("should return xhtml as primary when only xhtml present", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{{"report.xhtml", "<html/>"}})

			// Act
			entries, totalFiles, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, 1, totalFiles)
			assert.Equal(t, "report.xhtml", entries[0].Filename)
			assert.Equal(t, "application/xhtml+xml", entries[0].ContentType)
			assert.Equal(t, "<html/>", string(entries[0].Content))
			assert.True(t, entries[0].IsPrimary)
		})

		t.Run("should prefer xhtml over pdf when both present", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"filing.pdf", "PDF content"},
				{"report.xhtml", "<html/>"},
			})

			// Act
			entries, totalFiles, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 2)
			assert.Equal(t, 2, totalFiles)
			assert.True(t, entries[0].IsPrimary)
			assert.Equal(t, "report.xhtml", entries[0].Filename)
			assert.False(t, entries[1].IsPrimary)
			assert.Equal(t, "filing.pdf", entries[1].Filename)
		})

		t.Run("should select largest xhtml when multiple present", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"small.xhtml", "<s/>"},
				{"large.xhtml", "<large>lots of content here</large>"},
			})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 2)
			assert.Equal(t, "large.xhtml", entries[0].Filename)
			assert.True(t, entries[0].IsPrimary)
		})

		t.Run("should prefer html over pdf", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"filing.pdf", "PDF content"},
				{"report.html", "<html/>"},
			})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 2)
			assert.Equal(t, "report.html", entries[0].Filename)
			assert.Equal(t, "text/html", entries[0].ContentType)
			assert.True(t, entries[0].IsPrimary)
		})

		t.Run("should fall back to largest file when no recognised extension", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{{"data.bin", "binary content"}})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "data.bin", entries[0].Filename)
			assert.Equal(t, "application/octet-stream", entries[0].ContentType)
			assert.True(t, entries[0].IsPrimary)
		})

		t.Run("should return base filename not full zip path", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{{"dir/subdir/report-2024-T01.xhtml", "<html/>"}})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "report-2024-T01.xhtml", entries[0].Filename)
		})

		t.Run("should treat backslashes as archive separators", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{{`dir\subdir\report.xhtml`, "<html/>"}})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "report.xhtml", entries[0].Filename)
		})

		t.Run("should return error for entry with unusable base name", func(t *testing.T) {
			t.Parallel()

			// Arrange — zip entry whose base name is ".." (path.Base would return "..")
			zipData := buildZip(t, [][2]string{{"..", "content"}})

			// Act
			_, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			assert.ErrorContains(t, err, "unsafe filename")
		})

		t.Run("should reject unsafe archive paths", func(t *testing.T) {
			t.Parallel()

			tests := []struct {
				name     string
				filename string
			}{
				{name: "empty", filename: ""},
				{name: "current_directory", filename: "."},
				{name: "parent_directory", filename: ".."},
				{name: "slash_traversal", filename: "../../index.db"},
				{name: "backslash_traversal", filename: `..\..\index.db`},
				{name: "absolute", filename: "/tmp/report.xhtml"},
				{name: "drive_absolute", filename: `C:\temp\report.xhtml`},
				{name: "drive_relative", filename: `C:report.xhtml`},
				{name: "unc", filename: `\\server\share\report.xhtml`},
				{name: "repeated_separator", filename: "dir//report.xhtml"},
				{name: "trailing_backslash", filename: `dir\`},
				{name: "embedded_current_directory", filename: "dir/./report.xhtml"},
				{name: "embedded_parent_directory", filename: "dir/../report.xhtml"},
			}

			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					t.Parallel()

					// Arrange
					zipData := buildZip(t, [][2]string{{test.filename, "content"}})

					// Act
					_, _, err := archive.ExtractAll(zipData, limit)

					// Assert
					assert.ErrorContains(t, err, "unsafe filename")
				})
			}
		})
	})

	t.Run("multi-document", func(t *testing.T) {
		t.Parallel()

		t.Run("should return all entries with primary first", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"report.xhtml", "<xhtml/>"},
				{"report.pdf", "PDF"},
				{"readme.txt", "text"},
			})

			// Act
			entries, totalFiles, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.Len(t, entries, 3)
			assert.Equal(t, 3, totalFiles)
			assert.True(t, entries[0].IsPrimary, "first entry should be primary")
			assert.Equal(t, "report.xhtml", entries[0].Filename)
			primaryCount := 0
			for _, e := range entries {
				if e.IsPrimary {
					primaryCount++
				}
			}
			assert.Equal(t, 1, primaryCount, "exactly one entry should be primary")
		})

		t.Run("should deduplicate entries that share a base name", func(t *testing.T) {
			t.Parallel()

			// Arrange — two entries with different paths but the same base name.
			// The first entry (in zip order) is larger so it is also selected as primary.
			zipData := buildZip(t, [][2]string{
				{"subdir1/report.xhtml", "<primary>large enough to win</primary>"},
				{"subdir2/report.xhtml", "<s/>"},
			})

			// Act
			entries, totalFiles, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, 2, totalFiles)
			require.Len(t, entries, 2)
			// The first occurrence (in zip file order) keeps its original name.
			assert.Equal(t, "report.xhtml", entries[0].Filename)
			// The second occurrence gets the _2 suffix.
			assert.Equal(t, "report_2.xhtml", entries[1].Filename)
		})

		t.Run("should avoid collisions with a generated suffix", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"report.xhtml", "primary content is largest"},
				{"report.xhtml", "duplicate"},
				{"report_2.xhtml", "natural"},
			})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{"report.xhtml", "report_2.xhtml", "report_2_2.xhtml"}, []string{
				entries[0].Filename,
				entries[1].Filename,
				entries[2].Filename,
			})
			assert.True(t, entries[0].IsPrimary)
		})

		t.Run("should retry suffixes already used by natural filenames", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"report.xhtml", "primary content is largest"},
				{"report_2.xhtml", "natural"},
				{"report.xhtml", "duplicate one"},
				{"report.xhtml", "duplicate two"},
			})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{"report.xhtml", "report_2.xhtml", "report_3.xhtml", "report_4.xhtml"}, []string{
				entries[0].Filename,
				entries[1].Filename,
				entries[2].Filename,
				entries[3].Filename,
			})
		})

		t.Run("should preserve multiple extensions while allocating suffixes", func(t *testing.T) {
			t.Parallel()

			// Arrange
			zipData := buildZip(t, [][2]string{
				{"report.tar.gz", "primary content is largest"},
				{"report.tar.gz", "duplicate"},
				{"report.tar_2.gz", "natural"},
			})

			// Act
			entries, _, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.Equal(t, []string{"report.tar.gz", "report.tar_2.gz", "report.tar_2_2.gz"}, []string{
				entries[0].Filename,
				entries[1].Filename,
				entries[2].Filename,
			})
		})

		t.Run("should cap at MaxEntries and report true total", func(t *testing.T) {
			t.Parallel()

			// Arrange — build a zip with MaxEntries+5 files
			const extra = 5
			files := make([][2]string, archive.MaxEntries+extra)
			files[0] = [2]string{"primary.xhtml", "<xhtml/>"}
			for i := 1; i < len(files); i++ {
				files[i] = [2]string{
					strings.Repeat("a", i) + ".txt",
					"content",
				}
			}
			zipData := buildZip(t, files)

			// Act
			entries, totalFiles, err := archive.ExtractAll(zipData, limit)

			// Assert
			require.NoError(t, err)
			assert.LessOrEqual(t, len(entries), archive.MaxEntries)
			assert.Equal(t, archive.MaxEntries+extra, totalFiles)
		})
	})

	t.Run("zip bomb defence", func(t *testing.T) {
		t.Parallel()

		t.Run("should reject zip whose total uncompressed size exceeds limit", func(t *testing.T) {
			t.Parallel()

			// Arrange — use a real payload against a tiny limit
			const tinyLimit = 10
			zipData := buildZip(t, [][2]string{{"report.xhtml", "more than ten bytes of content here"}})

			// Act
			_, _, err := archive.ExtractAll(zipData, tinyLimit)

			// Assert
			assert.ErrorContains(t, err, "exceeds")
		})
	})

	t.Run("should return error for empty zip", func(t *testing.T) {
		t.Parallel()

		// Arrange
		zipData := buildZip(t, nil)

		// Act
		_, _, err := archive.ExtractAll(zipData, limit)

		// Assert
		assert.Error(t, err)
	})

	t.Run("should return error for malformed zip", func(t *testing.T) {
		t.Parallel()

		// Arrange — not a valid zip
		notAZip := []byte("PK\x03\x04this is not a real zip archive")

		// Act
		_, _, err := archive.ExtractAll(notAZip, limit)

		// Assert
		assert.ErrorContains(t, err, "open zip")
	})
}

func TestExtractAllManyDuplicateNames(t *testing.T) {
	t.Parallel()

	const count = 10_000
	files := make([][2]string, count)
	for i := range files {
		files[i] = [2]string{"report.xhtml", ""}
	}
	zipData := buildZip(t, files)

	entries, total, err := archive.ExtractAll(zipData, 0)

	require.NoError(t, err)
	assert.Equal(t, count, total)
	want := make([]archive.Entry, archive.MaxEntries)
	for i := range want {
		name := "report.xhtml"
		if i > 0 {
			name = fmt.Sprintf("report_%d.xhtml", i+1)
		}
		want[i] = archive.Entry{
			Filename: name, Content: []byte{}, ContentType: "application/xhtml+xml", IsPrimary: i == 0,
		}
	}
	assert.Equal(t, want, entries)
}

func BenchmarkExtractAllDuplicateNames(b *testing.B) {
	for _, count := range []int{1_000, 5_000, 10_000} {
		b.Run(fmt.Sprintf("entries_%d", count), func(b *testing.B) {
			var buf bytes.Buffer
			w := zip.NewWriter(&buf)
			for range count {
				if _, err := w.CreateHeader(&zip.FileHeader{Name: "report.xhtml", Method: zip.Store}); err != nil {
					b.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}
			zipData := buf.Bytes()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, _, err := archive.ExtractAll(zipData, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestReadBody(t *testing.T) {
	t.Parallel()

	t.Run("should return data when body is within limit", func(t *testing.T) {
		t.Parallel()

		// Arrange
		const limit = 100
		body := strings.NewReader("small content")

		// Act
		data, err := archive.ReadBody(body, limit)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, "small content", string(data))
	})

	t.Run("should return ErrBodyTooLarge when body exceeds limit", func(t *testing.T) {
		t.Parallel()

		// Arrange
		const limit = 10
		oversized := bytes.Repeat([]byte{'X'}, int(limit)+5)

		// Act
		_, err := archive.ReadBody(bytes.NewReader(oversized), limit)

		// Assert
		assert.ErrorIs(t, err, archive.ErrBodyTooLarge)
	})

	t.Run("should return a wrapped error when the reader fails", func(t *testing.T) {
		t.Parallel()

		// Act
		_, err := archive.ReadBody(errReader{}, 1024*1024)

		// Assert
		assert.ErrorContains(t, err, "read zip body")
	})
}
