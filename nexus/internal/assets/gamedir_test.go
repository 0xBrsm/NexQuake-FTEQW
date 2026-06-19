package assets

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestListMods_IncludesEmptyModDirs(t *testing.T) {
	gameDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(gameDir, "id1", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, "mod2", "server"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, "cfgmod"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, "junk"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "junk", "note.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, ".hidden", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, "thismodnameistoolong", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(gameDir, "bad\tname", "common"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mods, err := ListMods(gameDir)
	if err != nil {
		t.Fatalf("ListMods: %v", err)
	}

	if !slices.Contains(mods, "id1") || !slices.Contains(mods, "mod2") || !slices.Contains(mods, "cfgmod") {
		t.Fatalf("expected id1, mod2, and cfgmod, got %v", mods)
	}
	if slices.Contains(mods, "junk") {
		t.Fatalf("did not expect junk dir, got %v", mods)
	}
	if slices.Contains(mods, ".hidden") {
		t.Fatalf("did not expect hidden dir, got %v", mods)
	}
	if slices.Contains(mods, "thismodnameistoolong") {
		t.Fatalf("did not expect >15-byte mod name, got %v", mods)
	}
	if slices.Contains(mods, "bad\tname") {
		t.Fatalf("did not expect control-char mod name, got %v", mods)
	}
}
