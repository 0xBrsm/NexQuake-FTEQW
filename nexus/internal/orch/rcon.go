package orch

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	errUnknownServerInstance = errors.New("unknown server instance")

	// serverCommandCaptureSafetyTimeout bounds how long Nexus waits for an END
	// sentinel before giving up. It is a safety net for hung servers, not a
	// hint that capture should return earlier — replies are bounded by the END
	// marker, not by output rate.
	serverCommandCaptureSafetyTimeout = 5 * time.Second
)

func trimTrailingCommandSemicolons(cmd string) string {
	out := strings.TrimSpace(cmd)
	for strings.HasSuffix(out, ";") {
		out = strings.TrimSpace(strings.TrimSuffix(out, ";"))
	}
	return out
}

func sanitizeServerAuditText(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\"", "'")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

// formatServerCommandAuditEcho produces an `echo "<actor>: <cmd>"` preamble
// that runs before the BEGIN sentinel. Its output lands in the on-disk server
// log so operators can see who ran what; it is intentionally outside the
// captured reply window.
func formatServerCommandAuditEcho(cmd, actorID string) string {
	execCmd := trimTrailingCommandSemicolons(cmd)
	if execCmd == "" {
		return ""
	}
	actor := sanitizeServerAuditText(actorID)
	if actor == "" {
		actor = "admin"
	}
	auditCmd := sanitizeServerAuditText(execCmd)
	return fmt.Sprintf(`echo "%s: %s"`, actor, auditCmd)
}

func formatServerCommandReply(output string) string {
	output = strings.ReplaceAll(output, "\r", "")
	if strings.TrimSpace(output) == "" {
		return "Command executed successfully.\n"
	}
	if strings.HasSuffix(output, "\n") {
		return output
	}
	return output + "\n"
}

// serverCommandNoiseFilter drops the same low-signal lines the in-game console
// relay drops. The verbatim PTY echo of the command we just wrote is handled
// separately via the suppressed-relay-echo map, so it is not the filter's
// concern.
func serverCommandNoiseFilter(line string) (string, bool) {
	if !shouldRelayServerConsoleLine(line) {
		return "", false
	}
	return line, true
}

func parseServerTailCommand(cmd string) (bool, error) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return false, nil
	}
	if !strings.EqualFold(fields[0], "tail") {
		return false, nil
	}

	if len(fields) != 1 {
		return true, fmt.Errorf("usage: rcon <host|port|idx> tail")
	}
	return true, nil
}

const defaultServerTailLines = 10

func formatServerTailReply(lines []string) string {
	if len(lines) == 0 {
		return "No buffered console output.\n"
	}
	var out strings.Builder
	for _, line := range lines {
		line = strings.ReplaceAll(line, "\r", "")
		if line == "" {
			continue
		}
		out.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			out.WriteByte('\n')
		}
	}
	if out.Len() == 0 {
		return "No buffered console output.\n"
	}
	return out.String()
}

func captureManagedServerCommand(srv *managedServer, cmd, actorID string) (string, error) {
	if srv == nil {
		return "", errUnknownServerInstance
	}
	if srv.Console == nil {
		return "", fmt.Errorf("server console unavailable")
	}
	if isTail, tailErr := parseServerTailCommand(cmd); isTail {
		if tailErr != nil {
			return "", tailErr
		}
		lines := srv.Console.tail(defaultServerTailLines, func(line string) (string, bool) {
			return line, shouldRelayServerConsoleLine(line)
		})
		return formatServerTailReply(lines), nil
	}

	preamble := ""
	if strings.TrimSpace(actorID) != "" {
		preamble = formatServerCommandAuditEcho(cmd, actorID)
	}
	output, err := srv.Console.captureCommandBetweenSentinels(
		preamble,
		cmd,
		serverCommandCaptureSafetyTimeout,
		serverCommandNoiseFilter,
	)
	if err != nil {
		return "", err
	}
	return formatServerCommandReply(output), nil
}

func (m *ServerManager) instanceByListenPortLocked(port int) *instance {
	for _, serverID := range m.instanceIDsByPort[port] {
		rec := m.instancesByID[serverID]
		if m.instanceRunningLocked(rec) {
			return rec
		}
	}
	return nil
}

// DispatchInstanceCmd runs a console command against the running instance on
// the given listen port. Port is the only valid addressing scheme — hostnames
// and registry indices are not accepted.
func (m *ServerManager) DispatchInstanceCmd(port int, cmd, actorID string) (string, error) {
	if port < 1 || port > 65535 {
		return "", errUnknownServerInstance
	}

	m.mu.RLock()
	rec := m.instanceByListenPortLocked(port)
	var srv *managedServer
	if rec != nil && m.instanceRunningLocked(rec) {
		srv = rec.Running
	}
	m.mu.RUnlock()

	if srv == nil {
		return "", errUnknownServerInstance
	}
	return captureManagedServerCommand(srv, cmd, actorID)
}
