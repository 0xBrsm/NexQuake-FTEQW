package assets

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func copyToFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	_, err = io.Copy(f, r)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func extractZipGeneric(zr *zip.Reader, destRoot string) error {
	stripPrefix := zipSingleRootPrefix(zr)

	for _, zf := range zr.File {
		name := normalizedZipEntryName(zf)
		if name == "" {
			continue
		}
		if stripPrefix != "" && strings.HasPrefix(name, stripPrefix) {
			name = strings.TrimPrefix(name, stripPrefix)
		}
		if name == "" {
			continue
		}

		destPath, err := safeJoin(destRoot, name)
		if err != nil {
			return err
		}

		if err := copyZipEntry(zf, destPath); err != nil {
			return err
		}
	}
	return nil
}

func zipSingleRootPrefix(zr *zip.Reader) string {
	root := ""
	sawFile := false

	for _, zf := range zr.File {
		name := normalizedZipEntryName(zf)
		if name == "" {
			continue
		}

		sawFile = true
		slash := strings.IndexByte(name, '/')
		if slash <= 0 {
			// At least one file already at archive root: keep paths as-is.
			return ""
		}

		part := name[:slash]
		if root == "" {
			root = part
			continue
		}
		if part != root {
			// Multiple different top-level roots: keep paths as-is.
			return ""
		}
	}

	if !sawFile || root == "" {
		return ""
	}
	return root + "/"
}

func normalizedZipEntryName(zf *zip.File) string {
	name := strings.TrimLeft(strings.ReplaceAll(zf.Name, `\`, `/`), "/")
	if name == "" || strings.HasSuffix(name, "/") || zf.FileInfo().IsDir() {
		return ""
	}
	return name
}

func copyZipEntry(zf *zip.File, destPath string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return copyToFile(destPath, rc)
}

func dirHasEntries(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()

	_, err = f.Readdirnames(1)
	return err == nil
}

func safeJoin(root, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid path: %q", rel)
	}
	rootClean := filepath.Clean(root)
	path := filepath.Join(rootClean, clean)
	rootPrefix := rootClean + string(filepath.Separator)
	if path != rootClean && !strings.HasPrefix(path+string(filepath.Separator), rootPrefix) {
		return "", fmt.Errorf("path escape: %q", rel)
	}
	return path, nil
}

func sha256FileHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
