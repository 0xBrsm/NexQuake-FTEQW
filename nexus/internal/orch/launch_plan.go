package orch

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/google/shlex"
)

// serverLaunch is the parsed launch spec for a single server entry.
type serverLaunch struct {
	Line   int      // 0-based servers.ini line index; -1 for replicas
	LogDir string   // subdirectory name under logsDir for this server's log file
	Binary string   // path or name of the server binary
	Args   []string // command-line arguments passed to the binary
}

// unsupportedLaunchArgs lists server launch arguments Nexus does not currently
// support. FTE requires -basedir and -game, so they are intentionally absent
// here and pass through to fteqw-sv unchanged.
var unsupportedLaunchArgs = map[string]struct{}{
	"-hipnotic": {},
	"-path":     {},
	"-rogue":    {},
}

// parseFixedListenPort extracts a pinned UDP listen port from launch args, if
// one is set explicitly: "-port N" / "+port N" / "+sv_port N" / "+set sv_port N"
// (or the bare "port" cvar). It returns false for an ephemeral/unset port (0 or
// absent), where the bound port must be discovered at runtime instead.
//
// fteqw-sv's startup banner and port cvar differ from nqserver's, so the
// console-based port resolver (monitorServerStartup) never fires for it. When
// the launch pins a port, reading it here lets the info poller reach the server
// without that resolver — otherwise awaitingServerInfo never clears and every
// healthy launch logs a spurious startup timeout.
func parseFixedListenPort(args []string) (int, bool) {
	atoiPort := func(s string) (int, bool) {
		p, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || p < 1 || p > 65535 {
			return 0, false
		}
		return p, true
	}
	for i := 0; i < len(args); i++ {
		switch strings.ToLower(strings.TrimSpace(args[i])) {
		case "-port", "+port", "+sv_port":
			if i+1 < len(args) {
				if p, ok := atoiPort(args[i+1]); ok {
					return p, true
				}
			}
		case "+set":
			if i+2 < len(args) {
				switch strings.ToLower(strings.TrimSpace(args[i+1])) {
				case "sv_port", "port":
					if p, ok := atoiPort(args[i+2]); ok {
						return p, true
					}
				}
			}
		}
	}
	return 0, false
}

func (m *ServerManager) planLaunches() ([]serverLaunch, []string, error) {
	iniPath := filepath.Join(m.gameDir, "servers.ini")
	startedAt := time.Now().UTC()
	entries, ok, err := loadServersIni(iniPath, startedAt)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		startTag := startedAt.Format("20060102T150405Z")
		binary := "fteqw-sv"
		entries = []serverLaunch{
			{
				Line:   0,
				LogDir: fmt.Sprintf("%d-%s-%s", 0, filepath.Base(binary), startTag),
				Binary: binary,
				Args:   []string{"-dedicated"},
			},
		}
		slog.Info(fmt.Sprintf("Launch plan not found at %s; launching default server", iniPath))
	} else {
		slog.Debug(fmt.Sprintf("Using launch plan: %s (%d server entries)", iniPath, len(entries)))
	}

	mods, err := assets.ListMods(m.gameDir)
	if err != nil {
		return nil, nil, err
	}
	return entries, mods, nil
}

func loadServersIni(iniPath string, startedAt time.Time) (entries []serverLaunch, found bool, err error) {
	st, err := os.Stat(iniPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("stat %s: %w", iniPath, err)
	}
	if st.IsDir() {
		return nil, false, fmt.Errorf("servers.ini path is a directory: %s", iniPath)
	}

	f, err := os.Open(iniPath)
	if err != nil {
		return nil, false, fmt.Errorf("open %s: %w", iniPath, err)
	}
	defer f.Close()

	startTag := startedAt.UTC().Format("20060102T150405Z")
	scanner := bufio.NewScanner(f)
	launchGroups := make(map[string][]string)
	launchLine := -1
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, ";") {
			continue
		}

		fields, err := shlex.Split(raw)
		if err != nil {
			return nil, true, fmt.Errorf("servers.ini line %d: %w", lineNo, err)
		}
		if len(fields) == 0 {
			continue
		}

		if strings.HasPrefix(fields[0], "@") {
			if len(fields[0]) <= 1 {
				return nil, true, fmt.Errorf("servers.ini line %d: invalid group name %q", lineNo, fields[0])
			}
			launchGroups[fields[0]] = append([]string(nil), fields[1:]...)
			continue
		}

		if len(launchGroups) != 0 {
			fields = mergeLaunchGroups(fields, launchGroups)
		}
		if len(fields) == 0 {
			continue
		}

		launchLine++

		binary := fields[0]
		args := applyLaunchArgTemplates(fields[1:])
		if unsupportedArg, ok := findUnsupportedLaunchArg(args); ok {
			slog.Warn(fmt.Sprintf("Skipping servers.ini line %d: %s is not currently supported", lineNo, unsupportedArg))
			continue
		}

		logDir := fmt.Sprintf("%d-%s-%s", launchLine, filepath.Base(binary), startTag)

		entries = append(entries, serverLaunch{
			Line:   launchLine,
			LogDir: logDir,
			Binary: binary,
			Args:   args,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, true, fmt.Errorf("read %s: %w", iniPath, err)
	}
	if len(entries) == 0 {
		return nil, true, fmt.Errorf("servers.ini has no server launch lines: %s", iniPath)
	}

	return entries, true, nil
}

func findUnsupportedLaunchArg(args []string) (string, bool) {
	for _, arg := range args {
		if _, unsupported := unsupportedLaunchArgs[arg]; unsupported {
			return arg, true
		}
	}
	return "", false
}

func applyLaunchArgTemplates(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	out := append([]string(nil), args...)
	seen := make(map[string]string)

	for i := 0; i < len(out); i++ {
		if !isLaunchKeyToken(out[i]) {
			continue
		}
		key := out[i][1:]
		if _, found := seen[key]; !found && i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			seen[key] = out[i+1]
		}
		for i+1 < len(out) && !isLaunchKeyToken(out[i+1]) {
			i++
		}
	}

	for i, token := range out {
		if !strings.HasPrefix(token, "%") {
			continue
		}
		if v, found := seen[token[1:]]; found {
			out[i] = v
		}
	}

	return out
}

func mergeLaunchGroups(fields []string, launchGroups map[string][]string) []string {
	if len(fields) == 0 || len(launchGroups) == 0 {
		return fields
	}

	// The launch line's explicit args win over group-provided defaults.
	explicitKeys := make(map[string]struct{})
	for i := 1; i < len(fields); i++ {
		token := fields[i]
		if !isLaunchKeyToken(token) {
			continue
		}
		explicitKeys[token[1:]] = struct{}{}
	}

	insertedKeys := make(map[string]struct{})
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		groupFields, ok := launchGroups[token]
		if !ok {
			out = append(out, token)
			continue
		}

		// Insert group fields at the reference point, but skip any keys already
		// provided by the launch line (or earlier groups).
		for j := 0; j < len(groupFields); j++ {
			token := groupFields[j]
			if !isLaunchKeyToken(token) {
				out = append(out, token)
				continue
			}

			key := token[1:]
			_, inExplicit := explicitKeys[key]
			_, inInserted := insertedKeys[key]
			if inExplicit || inInserted {
				for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
					j++
				}
				continue
			}
			insertedKeys[key] = struct{}{}

			out = append(out, token)
			for j+1 < len(groupFields) && !isLaunchKeyToken(groupFields[j+1]) {
				j++
				out = append(out, groupFields[j])
			}
		}
	}

	return out
}

func isLaunchKeyToken(token string) bool {
	return len(token) >= 2 && (token[0] == '-' || token[0] == '+')
}
