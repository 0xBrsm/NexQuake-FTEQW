package assets

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuickstartGame_CreatesFromCfgAndAppendsQuickstart(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99 -port 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"game":"ctf"},
  {"base":"id1"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "ctf")

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "@def -dedicated 99 -port 0") {
		t.Fatalf("expected default servers.ini to be copied, got:\n%s", got)
	}
	if strings.Contains(got, "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", got)
	}
	if !strings.Contains(got, "nqserver @def -game ctf") {
		t.Fatalf("expected ctf entry appended, got:\n%s", got)
	}
}

func TestQuickstartGame_NoOverwriteExistingServersIni(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[{"base":"id1"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "existing\n" {
		t.Fatalf("expected servers.ini to remain unchanged, got %q", string(b))
	}
}

func TestQuickstartGame_AllSelectsAllGamesInCatalogOrder(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1"},
  {"game":"arena"},
  {"game":"ctf"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUICKSTART", "all")

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", s)
	}
	if !strings.Contains(s, "nqserver @def -game arena") || !strings.Contains(s, "nqserver @def -game ctf") {
		t.Fatalf("expected both entries, got:\n%s", s)
	}
	if strings.Index(s, "nqserver @def -game arena") > strings.Index(s, "nqserver @def -game ctf") {
		t.Fatalf("expected catalog order (arena before ctf), got:\n%s", s)
	}
}

func TestQuickstartGame_SkipsInvalidQuickstartValues(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1"},
  {"game":"ctf"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "bogus,ctf,still-nope")

	var logs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", s)
	}
	if !strings.Contains(s, "nqserver @def -game ctf") {
		t.Fatalf("expected ctf entry appended, got:\n%s", s)
	}
	if strings.Contains(s, "bogus") || strings.Contains(s, "still-nope") {
		t.Fatalf("unexpected invalid quickstart entry in servers.ini:\n%s", s)
	}
	skipLogs := matching(logs, "quickstart: skipped QUICKSTART entry")
	if len(skipLogs) != 2 {
		t.Fatalf("expected 2 skip logs, got %d (all=%v)", len(skipLogs), logs)
	}
	if !strings.Contains(skipLogs[0], `"bogus"`) {
		t.Fatalf("unexpected first skip log: %q", skipLogs[0])
	}
	if !strings.Contains(skipLogs[1], `"still-nope"`) {
		t.Fatalf("unexpected second skip log: %q", skipLogs[1])
	}
}

func TestQuickstartGame_SkipsBaseNameInQuickstart(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1"},
  {"game":"ctf"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("QUICKSTART", "id1,ctf")

	var logs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", s)
	}
	if !strings.Contains(s, "nqserver @def -game ctf") {
		t.Fatalf("expected ctf quickstart entry appended, got:\n%s", s)
	}
	skipLogs := matching(logs, "quickstart: skipped QUICKSTART entry")
	if len(skipLogs) != 1 || !strings.Contains(skipLogs[0], `"id1"`) {
		t.Fatalf("unexpected skip logs: %v", skipLogs)
	}
}

func TestQuickstartGame_AlwaysBootstrapsBaseGame(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	id1AssetPath := filepath.Join(t.TempDir(), "id1.txt")
	if err := os.WriteFile(id1AssetPath, []byte("id1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctfAssetPath := filepath.Join(t.TempDir(), "ctf.txt")
	if err := os.WriteFile(ctfAssetPath, []byte("ctf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("@def -dedicated 99\nnqserver @def -game ctf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1","common":["file://`+id1AssetPath+`"]},
  {"game":"ctf","common":["file://`+ctfAssetPath+`"]}
]`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUICKSTART", "bogus")

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	if _, err := os.Stat(filepath.Join(gameDir, "id1", "common", "id1.txt")); err != nil {
		t.Fatalf("expected id1 bootstrap output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gameDir, "ctf", "common", "ctf.txt")); err != nil {
		t.Fatalf("expected ctf bootstrap output: %v", err)
	}
}

func TestQuickstartGame_LogsDownloadStartAndGameCompletion(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	id1AssetPath := filepath.Join(t.TempDir(), "id1.txt")
	if err := os.WriteFile(id1AssetPath, []byte("id1"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctfAssetPath := filepath.Join(t.TempDir(), "ctf.txt")
	if err := os.WriteFile(ctfAssetPath, []byte("ctf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("nqserver -game ctf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1","common":["file://`+id1AssetPath+`"]},
  {"game":"ctf","common":["file://`+ctfAssetPath+`"]}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	starts := matching(logs, "Quickstart: downloading")
	if len(starts) != 1 {
		t.Fatalf("expected one downloading log, got=%v", logs)
	}
	if !strings.Contains(starts[0], "2 game packs") {
		t.Fatalf("expected pack count in log, got=%q", starts[0])
	}
	var completes []string
	for _, e := range logs {
		if strings.Contains(e, "complete!") {
			completes = append(completes, e)
		}
	}
	if len(completes) != 2 {
		t.Fatalf("expected 2 complete logs, got %d (all=%v)", len(completes), logs)
	}
	if !strings.Contains(completes[0], "id1") || !strings.Contains(completes[1], "ctf") {
		t.Fatalf("unexpected complete logs: %v", completes)
	}
}

func TestQuickstartGame_IgnoresGameInMacroDefinition(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("@def -game ctf\nnqserver @def\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	if _, err := os.Stat(filepath.Join(gameDir, "ctf")); !os.IsNotExist(err) {
		t.Fatalf("ctf mod dir should not be bootstrapped from macro definition")
	}
}

func TestQuickstartGame_DoesNotLogMissingGameFromServersIni(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("nqserver -game not-in-catalog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[{"base":"id1"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	if len(logs) != 0 {
		t.Fatalf("expected no logs for missing servers.ini game, got %v", logs)
	}
}

func TestQuickstartGame_ResolvesRelativeCatalogSourceFromCFGDIR(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "config.cfg"), []byte("bind g +showscores\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, "servers.ini"), []byte("nqserver -game id1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1","client":["config.cfg"]}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(gameDir, "id1", "client", "config.cfg"))
	if err != nil {
		t.Fatalf("read copied config.cfg: %v", err)
	}
	if string(got) != "bind g +showscores\n" {
		t.Fatalf("copied config.cfg mismatch: got %q", string(got))
	}
}

func TestQuickstartGame_DefaultQuickstartIsFFA(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[
  {"base":"id1"},
  {"game":"ffa"}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", s)
	}
	if !strings.Contains(s, "nqserver @def -game ffa") {
		t.Fatalf("expected ffa quickstart entry appended, got:\n%s", s)
	}
}

func TestQuickstartGame_DefaultQuickstartLogsWhenFFAIsMissing(t *testing.T) {
	gameDir := t.TempDir()
	cfgDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(cfgDir, "servers.ini"), []byte("@def -dedicated 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "game.json"), []byte(`[{"base":"id1"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{entries: &logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if err := QuickstartGame(context.Background(), gameDir, cfgDir); err != nil {
		t.Fatalf("QuickstartGame: %v", err)
	}

	skipLogs := matching(logs, "quickstart: skipped QUICKSTART entry")
	if len(skipLogs) != 1 || !strings.Contains(skipLogs[0], `"ffa"`) {
		t.Fatalf("unexpected skip logs: %v", skipLogs)
	}

	b, err := os.ReadFile(filepath.Join(gameDir, "servers.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "nqserver @def -game id1") {
		t.Fatalf("expected base entries to be excluded from generated servers.ini, got:\n%s", string(b))
	}
}
