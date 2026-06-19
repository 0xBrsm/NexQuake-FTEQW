package assets

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type assetManifestEntry struct {
	Path    string
	backing assetBacking
}

type assetBacking struct {
	filePath string
	pakPath  string
	pakEntry pakFileEntry
}

// buildVFSManifestWithWarnings produces a sorted manifest of all files
// available for one mod. warnf may be nil; it is used by upper layers (e.g.
// the manifest gateway) to log non-fatal scan issues without spamming logs
// when the same scan is run from a hot path.
func buildVFSManifestWithWarnings(gameDir, mod string, pakCache *PakIndexCache, warnf func(string, ...any)) ([]assetManifestEntry, error) {
	layers := []string{"common", "client"}

	// Later layers overwrite earlier ones.
	byKey := make(map[string]assetManifestEntry)

	for _, layer := range layers {
		if err := overlayLayerIntoManifest(byKey, gameDir, mod, layer, pakCache, warnf); err != nil {
			return nil, err
		}
	}

	out := make([]assetManifestEntry, 0, len(byKey))
	for _, v := range byKey {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b assetManifestEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

func buildCDManifest(cdDir string) ([]assetManifestEntry, error) {
	st, err := os.Stat(cdDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, nil
	}

	var out []assetManifestEntry
	err = filepath.WalkDir(cdDir, func(full string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, relErr := filepath.Rel(cdDir, full)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !isCDTrackFile(rel) {
			return nil
		}

		out = append(out, assetManifestEntry{
			Path:    rel,
			backing: assetBacking{filePath: full},
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b assetManifestEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out, nil
}

func isCDTrackFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".ogg") || strings.HasSuffix(lower, ".mp3")
}

var pakNumberRE = regexp.MustCompile(`(?i)^pak(\d+)\.pak$`)

func overlayLayerIntoManifest(byKey map[string]assetManifestEntry, gameDir, mod, layer string, pakCache *PakIndexCache, warnf func(string, ...any)) error {
	root := filepath.Join(gameDir, mod, layer)
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}

	// Quake precedence (within a single gamedir):
	//   loose files > pak files
	// and pak order (within a gamedir):
	//   pak0 < pak1 < ... (higher numbers override lower ones).
	//
	// Implement this deterministically by:
	//  1) exploding paks in ascending order (later paks overwrite earlier)
	//  2) overlaying loose files last (loose overwrites pak entries)
	paks, err := listLayerPakFiles(root)
	if err != nil {
		return err
	}
	for _, pakRel := range paks {
		full := filepath.Join(root, pakRel)
		if err := explodePakIntoManifest(byKey, full, pakCache); err != nil {
			// Keep bootstrap resilient when optional/third-party paks are malformed.
			// Loose files (and other valid paks) still populate the manifest.
			if warnf != nil {
				warnf("skipping unreadable pak %q: %v", full, err)
			}
			continue
		}
	}

	return filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		relLower := strings.ToLower(rel)
		if strings.HasSuffix(relLower, ".pak") {
			// Root paks are indexed in a deterministic pass; nested paks are not part of the runtime VFS.
			if strings.Contains(rel, "/") {
				return fmt.Errorf("nested pak not supported: %s/%s/%s", mod, layer, rel)
			}
			// Root pak: already processed in the deterministic pak pass above.
			return nil
		}

		key := normalizeVFSKey(rel)
		if key == "" {
			return nil
		}

		byKey[key] = assetManifestEntry{
			Path:    key,
			backing: assetBacking{filePath: full},
		}
		return nil
	})
}

func listLayerPakFiles(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var paks []string
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".pak") {
			continue
		}
		paks = append(paks, name)
	}

	slices.SortFunc(paks, func(a, b string) int {
		ak, bk := pakSortKey(a), pakSortKey(b)
		if c := cmp.Compare(ak.group, bk.group); c != 0 {
			return c
		}
		if ak.group == 0 {
			if c := cmp.Compare(ak.num, bk.num); c != 0 {
				return c
			}
		}
		return cmp.Compare(ak.name, bk.name)
	})

	return paks, nil
}

type pakKey struct {
	group int // 0: pakN.pak numeric, 1: other *.pak
	num   int
	name  string
}

func pakSortKey(name string) pakKey {
	lower := strings.ToLower(strings.TrimSpace(name))
	m := pakNumberRE.FindStringSubmatch(lower)
	if len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return pakKey{group: 0, num: n, name: lower}
		}
	}
	return pakKey{group: 1, name: lower}
}

func explodePakIntoManifest(byKey map[string]assetManifestEntry, pakPath string, pakCache *PakIndexCache) error {
	if pakCache == nil {
		pakCache = NewPakIndexCache()
	}
	idx, err := pakCache.get(pakPath)
	if err != nil {
		return err
	}

	for _, entry := range idx.entries {
		key := normalizeVFSKey(entry.name)
		if key == "" {
			continue
		}
		byKey[key] = assetManifestEntry{
			Path:    key,
			backing: assetBacking{pakPath: pakPath, pakEntry: entry},
		}
	}
	return nil
}

// normalizeVFSKey cleans and lowercases a VFS path.
func normalizeVFSKey(p string) string {
	clean, err := cleanVFSPath(p)
	if err != nil {
		return ""
	}
	return strings.ToLower(clean)
}

// cleanVFSPath validates and cleans a VFS path, rejecting traversals and absolute paths.
func cleanVFSPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimLeft(p, "/")
	p = path.Clean(p)
	if p == "." {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "../") || p == ".." || strings.Contains(p, "/../") {
		return "", fmt.Errorf("path traversal")
	}
	return p, nil
}
