package assets

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeZipFile(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip file %q: %v", zipPath, err)
	}
	zw := zip.NewWriter(f)
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
	if err := f.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}
}

func TestInstallFromSource_CopiesNonZip(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "servers.ini")
	want := "nqserver -dedicated 16 -port 26000 -game id1\n"
	if err := os.WriteFile(srcPath, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	destRoot := t.TempDir()
	if err := installFromSource(context.Background(), "file://"+srcPath, destRoot); err != nil {
		t.Fatalf("installFromSource(non-zip): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destRoot, "servers.ini"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != want {
		t.Fatalf("copied file mismatch:\n got=%q\nwant=%q", string(got), want)
	}
}

func TestInstallFromSource_ExtractsZip(t *testing.T) {
	srcDir := t.TempDir()
	zipPath := filepath.Join(srcDir, "mod.zip")
	writeZipFile(t, zipPath, map[string]string{
		"mymod/progs.dat":  "progs",
		"mymod/maps/a.ent": "ent",
	})

	destRoot := t.TempDir()
	if err := installFromSource(context.Background(), "file://"+zipPath, destRoot); err != nil {
		t.Fatalf("installFromSource(zip): %v", err)
	}

	if !fileExists(filepath.Join(destRoot, "progs.dat")) {
		t.Fatalf("missing extracted progs.dat")
	}
	if !fileExists(filepath.Join(destRoot, "maps", "a.ent")) {
		t.Fatalf("missing extracted maps/a.ent")
	}
}

func TestInstallLayer_SkipsExistingLayerUnlessForced(t *testing.T) {
	gameDir := t.TempDir()
	srcPath := filepath.Join(t.TempDir(), "servers.ini")
	if err := os.WriteFile(srcPath, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destRoot := filepath.Join(gameDir, "id1", "common")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destRoot, "existing.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installLayer(context.Background(), gameDir, "id1", "common", []string{"file://" + srcPath}, false); err != nil {
		t.Fatalf("installLayer(no force): %v", err)
	}
	if fileExists(filepath.Join(destRoot, "servers.ini")) {
		t.Fatalf("expected layer install to be skipped when destination is non-empty")
	}

	if err := installLayer(context.Background(), gameDir, "id1", "common", []string{"file://" + srcPath}, true); err != nil {
		t.Fatalf("installLayer(force): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(destRoot, "servers.ini"))
	if err != nil {
		t.Fatalf("read forced install output: %v", err)
	}
	if string(got) != "first\n" {
		t.Fatalf("forced install content mismatch: got %q, want %q", string(got), "first\n")
	}
}
