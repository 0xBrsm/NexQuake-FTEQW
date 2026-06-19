package quake106

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestQuake106Extract(t *testing.T) {
	zr, err := zip.OpenReader(filepath.Join("..", "assets", "quake106.zip"))
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("quake106.zip not present; skipping extraction test")
		}
		t.Fatal(err)
	}
	defer zr.Close()
	if err := ExtractPak0(&zr.Reader, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
