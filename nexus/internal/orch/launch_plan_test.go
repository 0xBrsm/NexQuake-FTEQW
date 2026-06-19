package orch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedStartedAt = time.Date(2026, time.January, 8, 14, 5, 6, 0, time.UTC)

func loadServersINI(t *testing.T, content string) ([]serverLaunch, bool, error) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "servers.ini")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	return loadServersIni(path, fixedStartedAt)
}

func TestLoadServerLaunchesFromINI_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.ini")

	entries, found, err := loadServersIni(path, fixedStartedAt)
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing file")
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries for missing file, got entries=%d", len(entries))
	}
}

func TestLoadServerLaunchesFromINI_EmptyFileIsError(t *testing.T) {
	_, found, err := loadServersINI(t, " # comments only\n\n// and blanks\n; ini comment\n")
	if !found {
		t.Fatalf("expected found=true when servers.ini exists")
	}
	if err == nil {
		t.Fatalf("expected error for empty servers.ini")
	}
}

func TestLoadServerLaunchesFromINI_ParsesRawArgsUnchanged(t *testing.T) {
	entries, found, err := loadServersINI(t, "\n# launch list\n// also a comment\n; semicolon comment\nnqserver -dedicated 8 -game ctf\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", e.Binary)
	}
	if e.Line != 0 {
		t.Fatalf("expected line 0, got %d", e.Line)
	}
	if e.LogDir != "0-nqserver-20260108T140506Z" {
		t.Fatalf("expected log dir line-bin-timestamp, got %q", e.LogDir)
	}
	want := []string{"-dedicated", "8", "-game", "ctf"}
	if len(e.Args) != len(want) {
		t.Fatalf("expected raw args %v, got %v", want, e.Args)
	}
	for i := range want {
		if e.Args[i] != want[i] {
			t.Fatalf("expected raw args %v, got %v", want, e.Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_ExplicitPortTokenPassesThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -port 26076 +hostname alpha\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", entries[0].Binary)
	}
	if entries[0].Line != 0 {
		t.Fatalf("expected line 0 (entry order), got %d", entries[0].Line)
	}
	if entries[0].LogDir != "0-nqserver-20260108T140506Z" {
		t.Fatalf("expected nqserver log dir with entry-order id and timestamp, got %q", entries[0].LogDir)
	}
	if len(entries[0].Args) == 0 || entries[0].Args[0] != "-dedicated" {
		t.Fatalf("expected first token after binary to be first arg, got %v", entries[0].Args)
	}
}

func TestLoadServerLaunchesFromINI_IPXPortTokenPassesThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -ipxport 26031\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Line != 0 {
		t.Fatalf("expected line=0, got %d", entries[0].Line)
	}
}

func TestLoadServerLaunchesFromINI_RespectsQuotes(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated +hostname \"my server\" -game ctf\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", entries[0].Binary)
	}
	want := []string{"-dedicated", "+hostname", "my server", "-game", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_SubstitutesTemplateFromSeenFlag(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -game ctf +hostname %game\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"-game", "ctf", "+hostname", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_SubstitutesTemplateFromSeenPlusCommand(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver +hostname ctf -game %hostname\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"+hostname", "ctf", "-game", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_TemplateBeforeFlagStillSubstitutes(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver +hostname %game -game ctf\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"+hostname", "ctf", "-game", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_TemplateUsesFirstFlagMatch(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -game ctf -game arena +hostname %game\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"-game", "ctf", "-game", "arena", "+hostname", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_SubstitutesTemplateAfterGroupExpansion(t *testing.T) {
	content := strings.Join([]string{
		"@default -game ctf +hostname %game",
		"nqserver @default",
	}, "\n") + "\n"

	entries, found, err := loadServersINI(t, content)
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"-game", "ctf", "+hostname", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_ExpandsGroups(t *testing.T) {
	content := strings.Join([]string{
		"; defaults",
		"@default -port 0",
		"nqserver -dedicated 16 @default -game ctf",
	}, "\n") + "\n"

	entries, found, err := loadServersINI(t, content)
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	want := []string{"-dedicated", "16", "-port", "0", "-game", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_GroupAllowsEmptyValue(t *testing.T) {
	entries, found, err := loadServersINI(t, "@default\nnqserver -dedicated 8 @default -game ctf\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true when servers.ini exists")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := []string{"-dedicated", "8", "-game", "ctf"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_AllowsMissingPortValuePassThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -port\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Line != 0 {
		t.Fatalf("expected default line 0, got %d", entries[0].Line)
	}
	if !strings.Contains(strings.Join(entries[0].Args, " "), "-port") {
		t.Fatalf("expected malformed -port to pass through untouched, got %v", entries[0].Args)
	}
}

func TestLoadServerLaunchesFromINI_AllowsCustomBinary(t *testing.T) {
	entries, found, err := loadServersINI(t, "/opt/quake/customsv -dedicated 6 -game custommod\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Binary != "/opt/quake/customsv" {
		t.Fatalf("expected custom binary path, got %q", entries[0].Binary)
	}
	if entries[0].LogDir != "0-customsv-20260108T140506Z" {
		t.Fatalf("expected custom binary basename in log dir, got %q", entries[0].LogDir)
	}
	want := []string{"-dedicated", "6", "-game", "custommod"}
	if len(entries[0].Args) != len(want) {
		t.Fatalf("expected unchanged args %v, got %v", want, entries[0].Args)
	}
	for i := range want {
		if entries[0].Args[i] != want[i] {
			t.Fatalf("expected unchanged args %v, got %v", want, entries[0].Args)
		}
	}
}

func TestLoadServerLaunchesFromINI_SkipsUnsupportedFlags(t *testing.T) {
	// -basedir is required by FTE and must NOT be skipped; -rogue/-hipnotic/-path
	// remain unsupported and their lines are dropped.
	entries, found, err := loadServersINI(t, strings.Join([]string{
		"fteqw-sv -dedicated -rogue",
		"fteqw-sv -dedicated -hipnotic",
		"fteqw-sv -dedicated -path /id1 /ctf",
		"fteqw-sv -dedicated 8 -basedir / -game ctf",
	}, "\n")+"\n")
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected only supported entries to remain, got %d", len(entries))
	}
	if entries[0].Binary != "fteqw-sv" {
		t.Fatalf("expected supported entry binary fteqw-sv, got %q", entries[0].Binary)
	}
	// -basedir and -game must pass through unchanged for FTE.
	joined := strings.Join(entries[0].Args, " ")
	if !strings.Contains(joined, "-basedir") || !strings.Contains(joined, "-game") {
		t.Fatalf("expected -basedir and -game to pass through, got %v", entries[0].Args)
	}
}

func TestLoadServerLaunchesFromINI_AllUnsupportedIsError(t *testing.T) {
	_, found, err := loadServersINI(t, strings.Join([]string{
		"fteqw-sv -dedicated -rogue",
		"fteqw-sv -dedicated -hipnotic",
		"fteqw-sv -dedicated -path /id1 /ctf",
	}, "\n")+"\n")
	if !found {
		t.Fatalf("expected found=true")
	}
	if err == nil {
		t.Fatalf("expected error when all server lines are unsupported")
	}
}

func TestLoadServerLaunchesFromINI_LogDirIncludesLineBinaryTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.ini")
	content := "nqserver -dedicated 8 -game ctf\nnqserver -dedicated 12 -game ctf\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	startedAt := time.Date(2026, time.January, 8, 14, 5, 6, 0, time.UTC)
	entries, found, err := loadServersIni(path, startedAt)
	if err != nil {
		t.Fatalf("loadServersIni() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].LogDir != "0-nqserver-20260108T140506Z" {
		t.Fatalf("expected first log dir with line-bin-timestamp, got %q", entries[0].LogDir)
	}
	if entries[1].LogDir != "1-nqserver-20260108T140506Z" {
		t.Fatalf("expected second log dir with line-bin-timestamp, got %q", entries[1].LogDir)
	}
	if strings.Contains(strings.Join(entries[0].Args, " "), "-port") {
		t.Fatalf("expected first args to remain unchanged (no auto -port), got %v", entries[0].Args)
	}
	if strings.Contains(strings.Join(entries[1].Args, " "), "-port") {
		t.Fatalf("expected second args to remain unchanged (no auto -port), got %v", entries[1].Args)
	}
}

func TestPlanLaunches_DefaultWhenServersINIMissing(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil)

	launches, _, err := m.planLaunches()
	if err != nil {
		t.Fatalf("expected default launch when servers.ini missing, got %v", err)
	}
	if len(launches) != 1 {
		t.Fatalf("expected single default launch, got %d", len(launches))
	}
	if launches[0].Line != 0 {
		t.Fatalf("expected default line 0, got %d", launches[0].Line)
	}
	// planLaunches uses current time; validate shape instead of exact value.
	if !strings.HasPrefix(launches[0].LogDir, "0-fteqw-sv-") {
		t.Fatalf("expected default log dir format 0-fteqw-sv-<timestamp>, got %q", launches[0].LogDir)
	}
	if len(launches[0].Args) != 1 || launches[0].Args[0] != "-dedicated" {
		t.Fatalf("expected default launch to run dedicated with no -port/-game, got %v", launches[0].Args)
	}
}

func TestPlanLaunches_LeavesBareNQServerUnchanged(t *testing.T) {
	gameDir := t.TempDir()
	path := filepath.Join(gameDir, "servers.ini")
	content := "nqserver -dedicated 8 -game ctf\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	m := NewServerManager(gameDir, t.TempDir(), nil, nil)
	launches, _, err := m.planLaunches()
	if err != nil {
		t.Fatalf("planLaunches() error = %v", err)
	}
	if len(launches) != 1 {
		t.Fatalf("expected single launch entry, got %d", len(launches))
	}
	if launches[0].Binary != "nqserver" {
		t.Fatalf("expected bare nqserver to remain unchanged, got %q", launches[0].Binary)
	}
	if strings.Contains(strings.Join(launches[0].Args, " "), "-port") {
		t.Fatalf("expected args to remain unchanged (no auto -port), got %v", launches[0].Args)
	}
}

func TestParseFixedListenPort(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantPort int
		wantOK   bool
	}{
		{"set sv_port", []string{"-dedicated", "-game", "id1", "+set", "sv_port", "27500", "+map", "start"}, 27500, true},
		{"set port", []string{"+set", "port", "26010"}, 26010, true},
		{"dash port", []string{"-dedicated", "-port", "27600"}, 27600, true},
		{"plus sv_port", []string{"+sv_port", "27700"}, 27700, true},
		{"ephemeral zero", []string{"-port", "0"}, 0, false},
		{"absent", []string{"-dedicated", "-game", "id1", "+map", "start"}, 0, false},
		{"out of range", []string{"+set", "sv_port", "70000"}, 0, false},
		{"non numeric", []string{"+set", "sv_port", "abc"}, 0, false},
		{"missing value", []string{"+set", "sv_port"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port, ok := parseFixedListenPort(tc.args)
			if ok != tc.wantOK || port != tc.wantPort {
				t.Fatalf("parseFixedListenPort(%v) = (%d, %v), want (%d, %v)", tc.args, port, ok, tc.wantPort, tc.wantOK)
			}
		})
	}
}
