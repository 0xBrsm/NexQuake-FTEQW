package assets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildVFSManifest_LayersAndPakExplode(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(gameDir, mod, "common")
	clientDir := filepath.Join(gameDir, mod, "client")
	if err := os.MkdirAll(filepath.Join(commonDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(clientDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Loose file in common.
	if err := os.WriteFile(filepath.Join(commonDir, "foo.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Pak in common with gfx/palette.lmp.
	pakPath := filepath.Join(commonDir, "pak0.pak")
	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": []byte{0x10, 0x11},
	})

	// Override gfx/palette.lmp with a loose file in client.
	if err := os.WriteFile(filepath.Join(clientDir, "gfx", "palette.lmp"), []byte("override"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	manifest, err := buildVFSManifestWithWarnings(gameDir, mod, NewPakIndexCache(), nil)
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	get := func(p string) (assetManifestEntry, bool) {
		for _, e := range manifest {
			if e.Path == p {
				return e, true
			}
		}
		return assetManifestEntry{}, false
	}

	if e, ok := get("foo.txt"); !ok || e.backing.filePath != filepath.Join(commonDir, "foo.txt") {
		t.Fatalf("expected foo.txt from common, got: ok=%v entry=%+v", ok, e)
	}

	// Should come from client (override), not /pak-extract from common.
	if e, ok := get("gfx/palette.lmp"); !ok || e.backing.filePath != filepath.Join(clientDir, "gfx", "palette.lmp") {
		t.Fatalf("expected gfx/palette.lmp from client override, got: ok=%v entry=%+v", ok, e)
	}
}

func TestBuildVFSManifest_LooseBeatsPakWithinLayer(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(gameDir, mod, "common")
	if err := os.MkdirAll(filepath.Join(commonDir, "gfx"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Pak in common with gfx/palette.lmp.
	pakPath := filepath.Join(commonDir, "pak0.pak")
	writeTestPak(t, pakPath, map[string][]byte{
		"gfx/palette.lmp": []byte{0x10, 0x11},
	})

	// Loose file in the same gamedir should override pak.
	if err := os.WriteFile(filepath.Join(commonDir, "gfx", "palette.lmp"), []byte("override"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	manifest, err := buildVFSManifestWithWarnings(gameDir, mod, NewPakIndexCache(), nil)
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	var got assetManifestEntry
	found := false
	for _, e := range manifest {
		if e.Path == "gfx/palette.lmp" {
			got = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing gfx/palette.lmp")
	}
	if got.backing.filePath != filepath.Join(commonDir, "gfx", "palette.lmp") {
		t.Fatalf("expected loose file to win over pak, got backing=%+v", got.backing)
	}
}

func TestBuildVFSManifest_PakOrderWithinLayer(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(gameDir, mod, "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeTestPak(t, filepath.Join(commonDir, "pak0.pak"), map[string][]byte{
		"docs/readme.txt": []byte("from pak0"),
	})
	writeTestPak(t, filepath.Join(commonDir, "pak1.pak"), map[string][]byte{
		"docs/readme.txt": []byte("from pak1"),
	})

	manifest, err := buildVFSManifestWithWarnings(gameDir, mod, NewPakIndexCache(), nil)
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	var got assetManifestEntry
	found := false
	for _, e := range manifest {
		if e.Path == "docs/readme.txt" {
			got = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing docs/readme.txt")
	}
	if got.backing.pakPath != filepath.Join(commonDir, "pak1.pak") || got.backing.pakEntry.name != "docs/readme.txt" {
		t.Fatalf("expected pak1 to override pak0, got backing=%+v", got.backing)
	}
}

func TestBuildVFSManifest_IgnoresCorruptPak(t *testing.T) {
	gameDir := t.TempDir()
	mod := "id1"

	commonDir := filepath.Join(gameDir, mod, "common")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Write a malformed pak whose header points the directory beyond EOF.
	corrupt := []byte{
		'P', 'A', 'C', 'K',
		0x64, 0x00, 0x00, 0x00, // dir offset = 100
		0x40, 0x00, 0x00, 0x00, // dir length = 64
	}
	if err := os.WriteFile(filepath.Join(commonDir, "pak0.pak"), corrupt, 0o644); err != nil {
		t.Fatalf("write corrupt pak: %v", err)
	}

	if err := os.WriteFile(filepath.Join(commonDir, "config.cfg"), []byte("echo ok"), 0o644); err != nil {
		t.Fatalf("write loose file: %v", err)
	}

	manifest, err := buildVFSManifestWithWarnings(gameDir, mod, NewPakIndexCache(), nil)
	if err != nil {
		t.Fatalf("buildVFSManifest: %v", err)
	}

	found := false
	for _, e := range manifest {
		if e.Path == "config.cfg" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected loose file to remain available, got manifest=%+v", manifest)
	}
}

func TestBuildCDManifest_FiltersSortsAndFormatsURLs(t *testing.T) {
	cdDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cdDir, "02-intro.ogg"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cdDir, "ambient"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "ambient", "#03-boss.mp3"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdDir, "readme.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	manifest, err := buildCDManifest(cdDir)
	if err != nil {
		t.Fatalf("buildCDManifest: %v", err)
	}
	if len(manifest) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(manifest))
	}

	if manifest[0].Path != "02-intro.ogg" {
		t.Fatalf("manifest[0].Path = %q, want %q", manifest[0].Path, "02-intro.ogg")
	}
	if manifest[0].backing.filePath != filepath.Join(cdDir, "02-intro.ogg") {
		t.Fatalf("manifest[0] backing = %+v", manifest[0].backing)
	}
	if manifest[1].Path != "ambient/#03-boss.mp3" {
		t.Fatalf("manifest[1].Path = %q, want %q", manifest[1].Path, "ambient/#03-boss.mp3")
	}
	if manifest[1].backing.filePath != filepath.Join(cdDir, "ambient", "#03-boss.mp3") {
		t.Fatalf("manifest[1] backing = %+v", manifest[1].backing)
	}
}
