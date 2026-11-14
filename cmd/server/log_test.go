package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestNewLoggerPretty(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, "info", "pretty")

	log.Debug("hidden")
	log.Info("http_request", "method", "GET", "status", 404)

	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("debug line leaked at info level:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-terminal output must stay plain:\n%q", out)
	}

	line := strings.TrimSuffix(out, "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("expected exactly one line, got:\n%s", out)
	}
	for _, want := range []string{"INF", "http_request", "method=GET", "status=404"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q:\n%s", want, line)
		}
	}
	if !regexp.MustCompile(`^\d{2}-\d{2}-\d{4} \d{2}:\d{2}:\d{2}\.\d{3} INF `).MatchString(line) {
		t.Errorf("timestamp/level do not keep the project format:\n%s", line)
	}
}

func TestNewLoggerJSON(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, "warn", "json")

	log.Info("hidden")
	log.Warn("degraded", "component", "db")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one JSON line, got:\n%s", buf.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("invalid JSON log line %q: %v", lines[0], err)
	}
	if record["msg"] != "degraded" || record["level"] != "WARN" || record["component"] != "db" {
		t.Errorf("unexpected JSON record: %+v", record)
	}
}

func TestIsTerminalBuffer(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("buffer must not be treated as a terminal")
	}
}
