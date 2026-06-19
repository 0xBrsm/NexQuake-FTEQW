package assets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/shlex"
)

type gameDataEntry struct {
	Game   string   `json:"game,omitempty"`
	Base   string   `json:"base,omitempty"`
	Server []string `json:"server,omitempty"`
	Common []string `json:"common,omitempty"`
	Client []string `json:"client,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// QuickstartGame ensures GAME_DIR/servers.ini exists (created only if missing)
// and bootstraps missing mod data based on the -game values in servers.ini using
// CFG_DIR/game.json.
//
// servers.ini is treated as user-owned:
//   - If it exists, it is never modified.
//   - If it does not exist, it is created from CFG_DIR/servers.ini and populated
//     with one nqserver line for each valid QUICKSTART game entry
//     (QUICKSTART defaults to "ffa" when unset):
//     nqserver @def -game <game>
//
// Progress and warnings are emitted through log/slog.
func QuickstartGame(ctx context.Context, gameDir, cfgDir string) error {
	gameDir = strings.TrimSpace(gameDir)
	cfgDir = strings.TrimSpace(cfgDir)
	if gameDir == "" {
		return fmt.Errorf("GAME_DIR is empty")
	}
	if cfgDir == "" {
		return fmt.Errorf("CFG_DIR is empty")
	}

	catalog, err := loadGameCatalog(cfgDir)
	if err != nil {
		return err
	}

	serversPath := filepath.Join(gameDir, "servers.ini")
	st, err := os.Stat(serversPath)
	switch {
	case err == nil:
		if st.IsDir() {
			return fmt.Errorf("servers.ini path is a directory: %s", serversPath)
		}
	case errors.Is(err, os.ErrNotExist):
		selected := selectGamesFromQuickstart(catalog)
		if err := createDefaultServersIni(gameDir, cfgDir, serversPath, selected); err != nil {
			return err
		}
	default:
		return fmt.Errorf("stat %s: %w", serversPath, err)
	}

	games, err := listGamesInServersIni(serversPath)
	if err != nil {
		return err
	}
	for _, ent := range catalog {
		if base := strings.TrimSpace(ent.Base); base != "" {
			games[base] = struct{}{}
		}
	}

	plan, pendingCount := quickstartInstallPlan(gameDir, catalog, games)
	if pendingCount > 0 {
		slog.Info(fmt.Sprintf("Quickstart: downloading %d game packs...", pendingCount))
	}

	for _, item := range plan {
		if err := os.MkdirAll(filepath.Join(gameDir, item.game), 0o755); err != nil {
			return fmt.Errorf("mkdir mod dir %q: %w", item.game, err)
		}

		for _, layer := range []struct {
			name    string
			sources []string
		}{
			{"common", item.entry.Common},
			{"server", item.entry.Server},
			{"client", item.entry.Client},
		} {
			if err := installLayer(ctx, gameDir, item.game, layer.name, layer.sources, item.entry.Force); err != nil {
				return fmt.Errorf("quickstart: %w", err)
			}
		}
		if item.pending {
			slog.Info(fmt.Sprintf("  %s complete!", item.game))
		}
	}

	return nil
}

type quickstartInstall struct {
	game    string
	entry   gameDataEntry
	pending bool
}

func quickstartInstallPlan(gameDir string, catalog []gameDataEntry, games map[string]struct{}) ([]quickstartInstall, int) {
	plan := make([]quickstartInstall, 0, len(catalog))
	seen := make(map[string]struct{}, len(catalog))
	var pendingCount int
	for _, ent := range catalog {
		game := catalogEntryName(ent)
		if game == "" {
			continue
		}
		if _, ok := seen[game]; ok {
			continue
		}
		seen[game] = struct{}{}
		if _, ok := games[game]; !ok {
			continue
		}
		pending := gameNeedsQuickstartDownload(gameDir, game, ent)
		if pending {
			pendingCount++
		}
		plan = append(plan, quickstartInstall{
			game:    game,
			entry:   ent,
			pending: pending,
		})
	}
	return plan, pendingCount
}

func createDefaultServersIni(gameDir, cfgDir, serversPath string, selected []string) error {
	if err := os.MkdirAll(gameDir, 0o755); err != nil {
		return fmt.Errorf("mkdir GAME_DIR: %w", err)
	}

	basePath := filepath.Join(cfgDir, "servers.ini")
	base, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", basePath, err)
	}

	var b strings.Builder
	b.Write(base)
	if len(base) != 0 && base[len(base)-1] != '\n' {
		b.WriteByte('\n')
	}
	for _, game := range selected {
		b.WriteString("nqserver @def -game ")
		b.WriteString(game)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(serversPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", serversPath, err)
	}
	return nil
}

func listGamesInServersIni(path string) (map[string]struct{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	out := make(map[string]struct{})
	lines := strings.Split(string(b), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}
		fields, err := shlex.Split(line)
		if err != nil || len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "@") {
			// Macro definition lines are launch templates, not server launches.
			continue
		}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "-game" {
				game := strings.TrimSpace(fields[i+1])
				if game != "" {
					out[game] = struct{}{}
				}
			}
		}
	}
	return out, nil
}

func loadGameCatalog(cfgDir string) ([]gameDataEntry, error) {
	path := filepath.Join(cfgDir, "game.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries []gameDataEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range entries {
		for _, sources := range []*[]string{&entries[i].Common, &entries[i].Server, &entries[i].Client} {
			for j := range *sources {
				(*sources)[j] = normalizeSource((*sources)[j], cfgDir)
			}
		}
	}
	return entries, nil
}

func normalizeSource(source, cfgDir string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") {
		return source
	}
	return "file://" + filepath.Clean(filepath.Join(cfgDir, source))
}

func selectGamesFromQuickstart(entries []gameDataEntry) []string {
	byName := make(map[string]struct{}, len(entries))
	order := make([]string, 0, len(entries))
	for _, ent := range entries {
		if name := strings.TrimSpace(ent.Game); name != "" {
			if _, ok := byName[name]; !ok {
				byName[name] = struct{}{}
				order = append(order, name)
			}
		}
	}

	raw := strings.TrimSpace(os.Getenv("QUICKSTART"))
	if raw == "" {
		raw = "ffa"
	}
	if strings.EqualFold(raw, "all") {
		// order is already deduplicated; return a copy directly.
		return append([]string(nil), order...)
	}

	selected := make([]string, 0, len(order))
	seen := make(map[string]struct{}, len(order))
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			slog.Info(fmt.Sprintf("quickstart: skipped QUICKSTART entry %q", name))
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		selected = append(selected, name)
	}
	return selected
}

func catalogEntryName(ent gameDataEntry) string {
	if base := strings.TrimSpace(ent.Base); base != "" {
		return base
	}
	return strings.TrimSpace(ent.Game)
}

func gameNeedsQuickstartDownload(gameDir, game string, ent gameDataEntry) bool {
	return layerNeedsInstall(gameDir, game, "common", ent.Common, ent.Force) ||
		layerNeedsInstall(gameDir, game, "server", ent.Server, ent.Force) ||
		layerNeedsInstall(gameDir, game, "client", ent.Client, ent.Force)
}

func layerNeedsInstall(gameDir, game, layer string, sources []string, force bool) bool {
	if len(sources) == 0 {
		return false
	}
	return force || !dirHasEntries(filepath.Join(gameDir, game, layer))
}
