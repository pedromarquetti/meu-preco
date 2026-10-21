package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 9, 15, 8, 11, 6, 356_000_000, time.Local)

// 02-01-2006 15:04:05.000

// emit writes a record with a fixed timestamp (Logger.Log always stamps
// time.Now, so tests bypass it and hand the handler a Record directly).
func emit(h slog.Handler, ts time.Time, level slog.Level, msg string, args ...slog.Attr) {
	r := slog.NewRecord(ts, level, msg, 0)
	r.AddAttrs(args...)
	_ = h.Handle(context.Background(), r)
}

func pairs(args ...any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		attrs = append(attrs, slog.Any(args[i].(string), args[i+1]))
	}
	return attrs
}

func TestHandleLineShape(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil)

	emit(h, fixedTime, slog.LevelInfo, "http_request",
		pairs("method", "GET", "path", "/", "status", 404)...,
	)

	got := buf.String()
	if !strings.HasPrefix(got, "[15-09-2026 08:11:06.356] INFO  http_request ") {
		t.Fatalf("bad line prefix:\n%q", got)
	}
	for _, want := range []string{"method=GET", "path=/", "status=404"} {
		if !strings.Contains(got, want) {
			t.Errorf("line missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("expected exactly one line, got %q", got)
	}
}

func TestAttrSorting(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil)

	emit(h, fixedTime, slog.LevelInfo, "m", pairs("zebra", 1, "alpha", 2)...)

	i := strings.Index(buf.String(), "alpha=2")
	j := strings.Index(buf.String(), "zebra=1")
	if i < 0 || j < 0 || i > j {
		t.Errorf("attrs not sorted: %q", buf.String())
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewPrettyHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	logger.Debug("hidden")
	logger.Info("hidden too")
	logger.Warn("shown")

	if strings.Contains(buf.String(), "hidden") {
		t.Errorf("filtered lines leaked:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "WARN  shown") {
		t.Errorf("warn line missing:\n%s", buf.String())
	}
}

func TestWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewPrettyHandler(&buf, nil)).With("session", "abc123")

	emit(logger.Handler(), fixedTime, slog.LevelInfo, "m")

	if !strings.Contains(buf.String(), "session=abc123") {
		t.Errorf("WithAttrs not rendered:\n%s", buf.String())
	}
}

func TestWithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil).WithGroup("req")

	emit(h, fixedTime, slog.LevelInfo, "m", pairs("path", "/x")...)

	if !strings.Contains(buf.String(), "req.path=/x") {
		t.Errorf("group not rendered:\n%s", buf.String())
	}
}

func TestGroupAttr(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil)

	emit(h, fixedTime, slog.LevelInfo, "m",
		slog.Attr{Key: "inner", Value: slog.GroupValue(slog.String("k", "v"))},
	)

	if !strings.Contains(buf.String(), "inner.k=v") {
		t.Errorf("group attr not rendered:\n%s", buf.String())
	}
}

func TestValueQuoting(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil)

	emit(h, fixedTime, slog.LevelInfo, "m",
		pairs("plain", "ok", "spaced", "has space", "empty", "")...,
	)

	got := buf.String()
	if !strings.Contains(got, "plain=ok") {
		t.Errorf("plain value misrendered: %s", got)
	}
	if !strings.Contains(got, "spaced=\"has space\"") {
		t.Errorf("spaced value not quoted: %s", got)
	}
	if !strings.Contains(got, "empty=\"\"") {
		t.Errorf("empty value not quoted: %s", got)
	}
}

func TestTimeFormatting(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrettyHandler(&buf, nil)

	emit(h, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), slog.LevelInfo, "m")
	emit(h, time.Time{}, slog.LevelInfo, "no-time")

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if !strings.HasPrefix(lines[0], "[") {
		t.Errorf("time missing on first line: %q", lines[0])
	}
	if strings.HasPrefix(lines[1], "[") {
		t.Errorf("time should be omitted for zero Time: %q", lines[1])
	}
}
