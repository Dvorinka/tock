package importer

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
)

// ZIP extraction limits (config/import.php upstream).
const (
	zipMaxFiles            = 10000
	zipMaxUncompressedSize = 500 * 1024 * 1024
)

// extractZip ports ZipImportHelper: validates entry names against zip-slip,
// enforces file-count and uncompressed-size limits both on the declared
// sizes and on actually-read bytes, and returns the archive as an in-memory
// file map instead of a temp directory.
func extractZip(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fail("Invalid ZIP, error code: 19")
	}
	if len(zr.File) > zipMaxFiles {
		return nil, fail("ZIP contains too many files, maximum is %d", zipMaxFiles)
	}
	var declared uint64
	for _, f := range zr.File {
		if err := validateZipName(f.Name); err != nil {
			return nil, err
		}
		declared += f.UncompressedSize64
		if declared > zipMaxUncompressedSize {
			return nil, fail("ZIP uncompressed size exceeds the maximum of %d bytes", zipMaxUncompressedSize)
		}
	}
	out := make(map[string][]byte, len(zr.File))
	var written uint64
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fail("ZIP entry %q can not be read", f.Name)
		}
		var buf bytes.Buffer
		for {
			n, err := io.CopyN(&buf, rc, 1<<20)
			written += uint64(n)
			if written > zipMaxUncompressedSize {
				_ = rc.Close()
				return nil, fail("ZIP uncompressed size exceeds the maximum of %d bytes", zipMaxUncompressedSize)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				_ = rc.Close()
				return nil, fail("ZIP entry %q can not be read", f.Name)
			}
		}
		_ = rc.Close()
		out[f.Name] = buf.Bytes()
	}
	return out, nil
}

func validateZipName(name string) error {
	bad := name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, "\\") ||
		strings.HasPrefix(name, "/") ||
		(len(name) >= 2 && name[1] == ':' && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')))
	if bad {
		return fail("ZIP contains an invalid file path: %q", name)
	}
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if seg == "" || seg == ".." {
			return fail("ZIP contains an invalid file path: %q", name)
		}
	}
	return nil
}
