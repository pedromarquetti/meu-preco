package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestServerGracefulShutdown builds the real binary, boots it on a free port,
// verifies /healthz answers, then sends SIGTERM and asserts a clean exit 0.
func TestServerGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "meupreco-server")
	build := exec.Command("go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	logFile, err := os.CreateTemp(t.TempDir(), "server-log-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = withEnv(os.Environ(), "PORT", freePort(t))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()

	if !waitReady(t, baseURL(t, cmd.Env), 20*time.Second) {
		t.Fatalf("server did not become ready\nlogs:\n%s", tailLog(logFile))
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean exit 0, got %v\nlogs:\n%s", err, tailLog(logFile))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("server did not exit after SIGTERM\nlogs:\n%s", tailLog(logFile))
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func baseURL(t *testing.T, env []string) string {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, "PORT=") {
			return "http://127.0.0.1:" + strings.TrimPrefix(kv, "PORT=")
		}
	}
	return "http://127.0.0.1:8080"
}

func withEnv(base []string, key, value string) []string {
	var out []string
	prefix := key + "="
	for _, kv := range base {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

func waitReady(t *testing.T, base string, timeout time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func tailLog(f *os.File) string {
	_ = f.Sync()
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Sprintf("<seek error: %v>", err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "<read error>"
	}
	return string(b)
}
