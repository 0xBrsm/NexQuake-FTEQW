package assets

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"testing"
)

func zipReaderFromFiles(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open in-memory zip: %v", err)
	}
	return zr
}

func TestExtractZipGeneric_StripsSingleRootDirectory(t *testing.T) {
	destRoot := t.TempDir()
	zr := zipReaderFromFiles(t, map[string]string{
		"3wave421x/progs.dat":     "progs",
		"3wave421x/maps/e1m1.ent": "ent",
	})

	if err := extractZipGeneric(zr, destRoot); err != nil {
		t.Fatalf("extractZipGeneric: %v", err)
	}

	if !fileExists(filepath.Join(destRoot, "progs.dat")) {
		t.Fatalf("missing flattened progs.dat")
	}
	if !fileExists(filepath.Join(destRoot, "maps", "e1m1.ent")) {
		t.Fatalf("missing flattened maps/e1m1.ent")
	}
	if fileExists(filepath.Join(destRoot, "3wave421x", "progs.dat")) {
		t.Fatalf("unexpected nested 3wave421x/progs.dat")
	}
}

func TestExtractZipGeneric_KeepsPathsWhenMultipleRoots(t *testing.T) {
	destRoot := t.TempDir()
	zr := zipReaderFromFiles(t, map[string]string{
		"id1/pak0.pak": "id1",
		"ctf/pak0.pak": "ctf",
	})

	if err := extractZipGeneric(zr, destRoot); err != nil {
		t.Fatalf("extractZipGeneric: %v", err)
	}

	if !fileExists(filepath.Join(destRoot, "id1", "pak0.pak")) {
		t.Fatalf("missing id1/pak0.pak")
	}
	if !fileExists(filepath.Join(destRoot, "ctf", "pak0.pak")) {
		t.Fatalf("missing ctf/pak0.pak")
	}
}
