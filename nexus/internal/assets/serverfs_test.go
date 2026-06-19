package assets

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func prepareOverlay(t *testing.T, gameDir, mod string) string {
	t.Helper()
	root, stop, err := PrepareRuntimeBasedir(gameDir, []string{mod})
	if err != nil {
		t.Fatalf("PrepareRuntimeBasedir: %v", err)
	}
	t.Cleanup(func() { stop(); _ = os.RemoveAll(root) })
	return root
}

func TestOverlay_ServerOverridesCommon_AndIgnoresRoot(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"
	commonDir := filepath.Join(gameDir, mod, "common")
	serverDir := filepath.Join(gameDir, mod, "server")
	for _, d := range []string{commonDir, serverDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(commonDir, "file.txt"), []byte("common"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "file.txt"), []byte("server"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, mod, "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runtimeRoot := prepareOverlay(t, gameDir, mod)

	got, err := os.ReadFile(filepath.Join(runtimeRoot, mod, "file.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "server" {
		t.Fatalf("expected server override, got %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, mod, "root.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected root.txt to be ignored, stat err=%v", err)
	}
}

func TestOverlay_DoesNotSymlinkDirs(t *testing.T) {
	gameDir := t.TempDir()
	mod := "ctf"
	commonDir := filepath.Join(gameDir, mod, "common", "maps")
	serverDir := filepath.Join(gameDir, mod, "server", "maps")
	for _, d := range []string{commonDir, serverDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(commonDir, "dm3.ent"), []byte("common"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "dm3.ent"), []byte("server"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runtimeRoot := prepareOverlay(t, gameDir, mod)

	mapsPath := filepath.Join(runtimeRoot, mod, "maps")
	if st, err := os.Lstat(mapsPath); err != nil {
		t.Fatalf("lstat maps: %v", err)
	} else if st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected maps dir to not be a symlink")
	}

	got, err := os.ReadFile(filepath.Join(runtimeRoot, mod, "maps", "dm3.ent"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "server" {
		t.Fatalf("expected server override, got %q", string(got))
	}
}

func TestOverlay_LowercasesRuntimePaths(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"
	commonDir := filepath.Join(gameDir, mod, "common")
	serverDir := filepath.Join(gameDir, mod, "server")
	for _, d := range []string{commonDir, serverDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(commonDir, "PROGS.DAT"), []byte("common"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "PROGS.DAT"), []byte("server"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runtimeRoot := prepareOverlay(t, gameDir, mod)

	got, err := os.ReadFile(filepath.Join(runtimeRoot, mod, "progs.dat"))
	if err != nil {
		t.Fatalf("read lowercase path: %v", err)
	}
	if string(got) != "server" {
		t.Fatalf("expected server override at lowercase path, got %q", string(got))
	}

	ents, err := os.ReadDir(filepath.Join(runtimeRoot, mod))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, ent := range ents {
		if ent.Name() == "PROGS.DAT" {
			t.Fatalf("expected uppercase runtime path to be absent, but it was found")
		}
	}
}

func TestOverlay_ReconcilesFileAddedAfterStart(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"
	mapsDir := filepath.Join(gameDir, mod, "common", "maps")
	if err := os.MkdirAll(mapsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mapsDir, "existing.bsp"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runtimeRoot := prepareOverlay(t, gameDir, mod)

	if err := os.WriteFile(filepath.Join(mapsDir, "fresh.bsp"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	target := filepath.Join(runtimeRoot, mod, "maps", "fresh.bsp")
	for time.Now().Before(deadline) {
		if got, err := os.ReadFile(target); err == nil && string(got) == "new" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("fresh.bsp did not appear in runtime within deadline")
}
