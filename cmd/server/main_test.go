package main

import (
	"bufio"
	"errors"
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

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"meupreco/migrations"
)

// TestServerGracefulShutdown builds the real binary, boots it on a free port,
// verifies /healthz answers, then sends SIGTERM and asserts a clean exit 0.
// It runs migrations on a test database (TEST_DATABASE_NAME) before starting
// and rolls them back after.
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

	env := loadEnv(root)

	username := env["POSTGRES_USERNAME"]
	if username == "" {
		username = "dev"
	}

	password := env["POSTGRES_PASSWORD"]
	if password == "" {
		password = "dev"
	}

	host := env["POSTGRES_HOST"]
	if host == "" {
		host = "127.0.0.1"
	}

	testDB := env["TEST_DATABASE_NAME"]
	if testDB == "" {
		testDB = "meupreco_test"
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", username, password, host, testDB)

	if err := migrateSchemaUp(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	t.Cleanup(func() {
		if err := migrateSchemaDown(dsn); err != nil {
			t.Logf("migrate down (cleanup): %v", err)
		}
	})

	cmd := exec.Command(bin)
	cmd.Env = withEnv(mapToEnv(env), "PORT", freePort(t))
	cmd.Env = withEnv(cmd.Env, "DATABASE_NAME", testDB)

	jwtSecret := env["JWT_SECRET"]
	if jwtSecret == "" {
		jwtSecret = "test-only-secret-with-at-least-32-chars"
	}
	cmd.Env = withEnv(cmd.Env, "JWT_SECRET", jwtSecret)

	adminEmail := env["ADMIN_EMAIL"]
	if adminEmail == "" {
		adminEmail = "test@example.com"
	}
	cmd.Env = withEnv(cmd.Env, "ADMIN_EMAIL", adminEmail)

	adminPassword := env["ADMIN_PASSWORD"]
	if adminPassword == "" {
		adminPassword = "test-password"
	}

	cmd.Env = withEnv(cmd.Env, "ADMIN_PASSWORD", adminPassword)
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

func loadEnv(root string) map[string]string {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.Index(kv, "="); i >= 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}

	envPath := filepath.Join(root, ".env")
	f, err := os.Open(envPath)
	if err != nil {
		return env
	}

	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if i := strings.Index(line, "="); i >= 0 {
			key := strings.TrimSpace(line[:i])
			val := strings.TrimSpace(line[i+1:])
			val = strings.Trim(val, `"'`)
			env[key] = val
		}
	}
	return env
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

func migrateSchemaUp(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func migrateSchemaDown(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func mapToEnv(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
