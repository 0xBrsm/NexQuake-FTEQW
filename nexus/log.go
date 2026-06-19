package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type logLevel int

const (
	logError logLevel = iota
	logWarn
	logInfo
	logDebug
)

const (
	nexusLogHistoryCap = 2048
	logTimestampLayout = "2006/01/02 15:04:05"
)

var (
	// currentLogLevel gates the operator console and the in-memory rcon tail.
	currentLogLevel logLevel = logInfo

	// fileLogLevel gates the on-disk nexus.log. It is held at logDebug so the
	// file is a full-fidelity record for postmortems regardless of how quiet
	// the operator console is set via LOG_LEVEL.
	fileLogLevel logLevel = logDebug

	operatorConsoleTimestamp atomic.Bool
	operatorConsoleMu        sync.Mutex

	nexusLogHistoryMu sync.RWMutex
	nexusLogHistory   []string

	nexusLogFileMu sync.Mutex
	nexusLogFile   *os.File

	logLineEndings = strings.NewReplacer("\r\n", "\n", "\r", "\n")
)

// initLogging configures the log level from LOG_LEVEL and applies the
// CONSOLE_TIMESTAMPS preference. Must be called once before any logf calls.
func initLogging() {
	operatorConsoleTimestamp.Store(true)
	applyOperatorConsoleTimestampEnv()

	level := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	switch level {
	case "", "info":
		currentLogLevel = logInfo
	case "debug":
		currentLogLevel = logDebug
	case "warn", "warning":
		currentLogLevel = logWarn
	case "error":
		currentLogLevel = logError
	default:
		currentLogLevel = logInfo
		slog.Warn(fmt.Sprintf("Unknown LOG_LEVEL=%q (expected: error|warn|info|debug); defaulting to info", level))
	}
}

// applyOperatorConsoleTimestampEnv reads CONSOLE_TIMESTAMPS (0|1) and updates
// the operatorConsoleTimestamp flag. Called at startup and exposed for testing.
func applyOperatorConsoleTimestampEnv() {
	raw := strings.TrimSpace(os.Getenv("CONSOLE_TIMESTAMPS"))
	if raw == "" {
		return
	}
	switch raw {
	case "1":
		operatorConsoleTimestamp.Store(true)
	case "0":
		operatorConsoleTimestamp.Store(false)
	default:
		slog.Warn(fmt.Sprintf("Unknown CONSOLE_TIMESTAMPS=%q (expected: 0|1); defaulting to 1", raw))
		operatorConsoleTimestamp.Store(true)
	}
}

// configureNexusLogFile opens (or creates) nexus.log inside logsDir and sets
// it as the active log destination. It is safe to call more than once; the
// previous file is closed atomically.
func configureNexusLogFile(logsDir string) error {
	logsDir = strings.TrimSpace(logsDir)
	if logsDir == "" {
		return fmt.Errorf("LOGS_DIR is empty")
	}
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(logsDir, "nexus.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	nexusLogFileMu.Lock()
	old := nexusLogFile
	nexusLogFile = f
	nexusLogFileMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// closeNexusLogFile closes the active nexus log file. Called via defer at shutdown.
func closeNexusLogFile() {
	nexusLogFileMu.Lock()
	f := nexusLogFile
	nexusLogFile = nil
	nexusLogFileMu.Unlock()
	if f != nil {
		_ = f.Close()
	}
}

func operatorConsoleTimestampsEnabled() bool {
	return operatorConsoleTimestamp.Load()
}

// formatTimestampedLogText prefixes every line of msg with a timestamp.
// The result is always newline-terminated; empty input returns "".
func formatTimestampedLogText(msg string, now time.Time) string {
	return formatLogLines(msg, now.Format(logTimestampLayout)+" ")
}

// formatLogLines re-emits msg with prefix prepended to each line. Returns ""
// for blank input; otherwise the result is newline-terminated.
func formatLogLines(msg, prefix string) string {
	lines := splitLogLines(msg)
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// splitLogLines normalises line endings and splits msg into individual lines,
// stripping trailing newlines. Returns nil for blank input.
func splitLogLines(msg string) []string {
	msg = strings.TrimRight(logLineEndings.Replace(msg), "\n")
	if msg == "" {
		return nil
	}
	return strings.Split(msg, "\n")
}

// writeOperatorConsoleText writes msg to stderr. The pre-formatted
// timestamped form is used when timestamp mode is enabled; otherwise msg is
// reformatted without timestamps. Lazy formatting avoids the second pass in
// the common (timestamps-on) case.
func writeOperatorConsoleText(timestamped, msg string) {
	operatorConsoleMu.Lock()
	defer operatorConsoleMu.Unlock()

	text := timestamped
	if !operatorConsoleTimestampsEnabled() {
		text = formatLogLines(msg, "")
	}
	if text == "" {
		return
	}
	_, _ = io.WriteString(os.Stderr, text)
}

// writeNexusLogFile appends text to the open nexus log file, if any.
func writeNexusLogFile(text string) {
	if text == "" {
		return
	}
	nexusLogFileMu.Lock()
	defer nexusLogFileMu.Unlock()
	if nexusLogFile == nil {
		return
	}
	_, _ = io.WriteString(nexusLogFile, text)
}

// logf formats and emits a log message at the given level. Messages are written
// to the in-memory tail buffer, the log file, and the operator console.
// Suppressed when level exceeds currentLogLevel.
// maxLogVerbosity returns the most-verbose level any sink wants, so callers can
// skip formatting a record only when every sink would drop it.
func maxLogVerbosity() logLevel {
	if currentLogLevel > fileLogLevel {
		return currentLogLevel
	}
	return fileLogLevel
}

func logf(level logLevel, format string, args ...any) {
	if level > maxLogVerbosity() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	logfSplit(level, msg, msg)
}

// logfSplit emits a structured log line with two routing axes. By detail:
// the operator console receives only consoleMsg (the bare slog message) while
// the file and tail buffer receive fullMsg (message + key=value attributes) —
// plain logf passes the same text for both. By level: the file captures down
// to fileLogLevel (full fidelity for postmortems) while the console and tail
// honor currentLogLevel (LOG_LEVEL), so the live console can stay quiet without
// losing detail on disk.
func logfSplit(level logLevel, consoleMsg, fullMsg string) {
	toFile := level <= fileLogLevel
	toConsole := level <= currentLogLevel
	if !toFile && !toConsole {
		return
	}
	now := time.Now()
	fullTimestamped := formatTimestampedLogText(fullMsg, now)
	if toFile {
		writeNexusLogFile(fullTimestamped)
	}
	if toConsole {
		recordNexusLogLine(fullTimestamped)
		writeOperatorConsoleText(formatTimestampedLogText(consoleMsg, now), consoleMsg)
	}
}

// logfNoTail is like logf but skips the tail buffer and file — used for
// server console relay lines that should appear on-screen but not pollute the
// admin tail history.
func logfNoTail(level logLevel, format string, args ...any) {
	if level > currentLogLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	writeOperatorConsoleText(formatTimestampedLogText(msg, time.Now()), msg)
}

// auditf always records the message to the tail buffer and log file regardless
// of the current log level. It also writes to the operator console when debug
// level is active. Use for security-sensitive events (bans, promotions, etc.).
func auditf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	auditfSplit(msg, msg)
}

// auditfSplit is logfSplit's always-recorded sibling for the audit logger: the
// full line (message + attrs) is recorded to the tail buffer and file
// regardless of level; the bare consoleMsg reaches the console only at debug.
func auditfSplit(consoleMsg, fullMsg string) {
	now := time.Now()
	fullTimestamped := formatTimestampedLogText(fullMsg, now)
	recordNexusLogLine(fullTimestamped)
	writeNexusLogFile(fullTimestamped)
	if currentLogLevel >= logDebug {
		writeOperatorConsoleText(formatTimestampedLogText(consoleMsg, now), consoleMsg)
	}
}
func infofNoTail(format string, args ...any) {
	logfNoTail(logInfo, format, args...)
}
func fatalf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
	os.Exit(1)
}

// recordNexusLogLine appends each line of text to the in-memory ring buffer.
// When the buffer is full (nexusLogHistoryCap) the oldest line is evicted.
func recordNexusLogLine(text string) {
	if text == "" {
		return
	}
	nexusLogHistoryMu.Lock()
	defer nexusLogHistoryMu.Unlock()
	for _, line := range splitLogLines(text) {
		if line == "" {
			continue
		}
		if len(nexusLogHistory) < nexusLogHistoryCap {
			nexusLogHistory = append(nexusLogHistory, line)
		} else {
			copy(nexusLogHistory, nexusLogHistory[1:])
			nexusLogHistory[len(nexusLogHistory)-1] = line
		}
	}
}

// tailNexusLogLines returns the last n lines from the in-memory log buffer.
// Returns nil when n <= 0 or the buffer is empty.
func tailNexusLogLines(n int) []string {
	if n <= 0 {
		return nil
	}
	nexusLogHistoryMu.RLock()
	defer nexusLogHistoryMu.RUnlock()
	if len(nexusLogHistory) == 0 {
		return nil
	}
	n = min(n, len(nexusLogHistory))
	return slices.Clone(nexusLogHistory[len(nexusLogHistory)-n:])
}

// initSlog wires log/slog into the existing logf/auditf core so internal
// packages can call slog.Info/Debug/Warn/Error directly without taking
// logger function references at construction. Call once at startup before
// any subsystem is built.
//
// Returns the audit logger so main can hand it to admin.New. The audit
// logger writes through auditf (always recorded regardless of level)
// rather than the level-gated logf used by the default logger.
func initSlog() *slog.Logger {
	slog.SetDefault(slog.New(&forwardHandler{
		emit: func(level slog.Level, consoleMsg, fullMsg string) {
			logfSplit(slogToLogLevel(level), consoleMsg, fullMsg)
		},
		enabled: func(level slog.Level) bool {
			return slogToLogLevel(level) <= maxLogVerbosity()
		},
	}))
	return slog.New(&forwardHandler{
		emit:    func(_ slog.Level, consoleMsg, fullMsg string) { auditfSplit(consoleMsg, fullMsg) },
		enabled: func(_ slog.Level) bool { return true },
	})
}

// forwardHandler is a slog.Handler that renders the record as
// "message k1=v1 k2=v2 ..." and hands the result to a printf-style emit
// function. Existing logf/auditf machinery owns timestamping, file write,
// tail buffer, and operator console — this handler just shapes the line.
type forwardHandler struct {
	emit    func(level slog.Level, consoleMsg, fullMsg string)
	enabled func(level slog.Level) bool
	attrs   []slog.Attr
	groups  []string
}

func (h *forwardHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.enabled(level)
}

func (h *forwardHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	prefix := strings.Join(h.groups, ".")
	for _, a := range h.attrs {
		appendAttr(&b, prefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, prefix, a)
		return true
	})
	h.emit(r.Level, r.Message, b.String())
	return nil
}

func (h *forwardHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *h
	out.attrs = append(slices.Clone(h.attrs), attrs...)
	return &out
}

func (h *forwardHandler) WithGroup(name string) slog.Handler {
	out := *h
	out.groups = append(slices.Clone(h.groups), name)
	return &out
}

func appendAttr(b *strings.Builder, groupPrefix string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	if groupPrefix != "" {
		b.WriteString(groupPrefix)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	fmt.Fprintf(b, "%v", a.Value.Any())
}

func slogToLogLevel(l slog.Level) logLevel {
	switch {
	case l >= slog.LevelError:
		return logError
	case l >= slog.LevelWarn:
		return logWarn
	case l >= slog.LevelInfo:
		return logInfo
	default:
		return logDebug
	}
}

// newServerErrorLog returns the *log.Logger to plug into http.Server.ErrorLog.
// It routes the server's internal error output through slog and demotes benign
// per-connection TLS handshake noise to debug.
//
// A public TLS endpoint is probed constantly by scanners that connect without
// SNI, speak SSLv2, offer no common cipher suite, or reset mid-handshake. Go
// logs each as "http: TLS handshake error from <ip>: ...". None are actionable,
// but at the default level they flood the log. Genuine problems stay visible:
// any non-handshake server error, and any ACME *issuance* failure (including a
// Let's Encrypt rate-limit), are logged at warn. The two scanner signatures
// that look ACME-shaped but name a host that is never ours — the SNI-less
// "missing server name" and the "not configured in HostWhitelist" rejection —
// are demoted to debug.
func newServerErrorLog() *log.Logger {
	return log.New(slogErrorWriter{}, "", 0)
}

type slogErrorWriter struct{}

func (slogErrorWriter) Write(p []byte) (int, error) {
	// http.Server logs one message per Write; trim its trailing newline and
	// classify the whole line — no need to split.
	line := string(bytes.TrimRight(p, "\n"))
	if line != "" {
		if isBenignHandshakeNoise(line) {
			slog.Debug(line)
		} else {
			slog.Warn(line)
		}
	}
	return len(p), nil
}

// isBenignHandshakeNoise reports whether an http.Server error line is a
// per-connection TLS handshake failure driven by the client (a scanner, a
// stray probe, an abrupt disconnect) rather than a server-side problem worth an
// operator's attention. A real cert-issuance failure — autocert could not serve
// a cert to a client that did send a valid SNI — is NOT noise and is excluded
// so it stays at warn.
func isBenignHandshakeNoise(line string) bool {
	if !strings.Contains(line, "TLS handshake error") {
		return false
	}
	// The SNI-less probe ("acme/autocert: missing server name") is the most
	// common scanner signature — benign.
	if strings.Contains(line, "missing server name") {
		return true
	}
	// A probe that sends an SNI for a host we don't serve trips autocert's
	// HostWhitelist ("acme/autocert: host %q not configured in HostWhitelist").
	// Our EXTERNAL_URL host is the only whitelisted name, so this message can
	// only ever name someone else's host — it's a scanner, not a cert-issuance
	// problem for us. Benign. (Checked before the acme/autocert: rule below,
	// which would otherwise promote it to warn and flood the log.)
	if strings.Contains(line, "not configured in HostWhitelist") {
		return true
	}
	// Any other ACME failure is actionable and must not be demoted. autocert
	// surfaces these two ways: wrapped ("acme/autocert: ...") and as a raw ACME
	// protocol error from Let's Encrypt ("...acme:error:..." / "rateLimited"),
	// e.g. the duplicate-certificate rate limit. Keep both at warn.
	if strings.Contains(line, "acme/autocert:") ||
		strings.Contains(line, "acme:error") ||
		strings.Contains(line, "rateLimited") {
		return false
	}
	return true
}
