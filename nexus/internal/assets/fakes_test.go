package assets

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// captureHandler is a slog.Handler that appends "msg key=value ..." lines to
// the given slice. Tests use it via slog.SetDefault to assert log output.
type captureHandler struct{ entries *[]string }

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	*h.entries = append(*h.entries, b.String())
	return nil
}

func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

// matching returns the subset of entries whose rendered text starts with
// prefix (the slog message portion). Used by tests asserting on specific
// log records produced amid possibly other slog output.
func matching(entries []string, prefix string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
