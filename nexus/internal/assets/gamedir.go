package assets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ListMods returns valid game directory names found under gameDir that either:
// - contain at least one layer directory (common, client, or server), or
// - are empty directories (used for client-only config mods with no data yet).
func ListMods(gameDir string) ([]string, error) {
	ents, err := os.ReadDir(gameDir)
	if err != nil {
		return nil, fmt.Errorf("read GAME_DIR: %w", err)
	}

	var mods []string
	for _, ent := range ents {
		name := ent.Name()
		if !ent.IsDir() || strings.HasPrefix(name, ".") || !isValidQuakeGameDirName(name) {
			continue
		}
		modDir := filepath.Join(gameDir, name)
		if dirHasAnyLayer(modDir) || dirIsEmpty(modDir) {
			mods = append(mods, name)
		}
	}
	return mods, nil
}

func isValidQuakeGameDirName(name string) bool {
	if len(name) == 0 || len(name) > 15 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] == 0x7f {
			return false
		}
	}
	return true
}

func dirHasAnyLayer(modDir string) bool {
	for _, layer := range []string{"common", "client", "server"} {
		st, err := os.Stat(filepath.Join(modDir, layer))
		if err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func dirIsEmpty(modDir string) bool {
	ents, err := os.ReadDir(modDir)
	return err == nil && len(ents) == 0
}
