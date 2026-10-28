package testdb

import (
	"strings"
	"testing"
)

func TestValidateTestDSN(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		wantErr string
	}{
		{
			name: "test database",
			dsn:  "postgres://user:pass@localhost:5432/meupreco_test?sslmode=disable",
		},
		{
			name:    "production database",
			dsn:     "postgres://user:pass@localhost:5432/meupreco?sslmode=disable",
			wantErr: "must end in _test",
		},
		{
			name:    "empty database",
			dsn:     "",
			wantErr: "must end in _test",
		},
		{
			name:    "invalid dsn",
			dsn:     "not-a-dsn",
			wantErr: "parse test database DSN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTestDSN(tt.dsn)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTestDSN() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateTestDSN() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateTestDSN() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRequireDSN(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv(dsnEnv, "")
		_, err := RequireDSN()
		if err == nil {
			t.Fatal("RequireDSN() error = nil, want an error")
		}
		if !strings.Contains(err.Error(), dsnEnv) {
			t.Fatalf("RequireDSN() error = %q, want it to mention %s", err, dsnEnv)
		}
	})

	t.Run("valid", func(t *testing.T) {
		const dsn = "postgres://user:pass@localhost:5432/meupreco_test?sslmode=disable"
		t.Setenv(dsnEnv, dsn)
		got, err := RequireDSN()
		if err != nil {
			t.Fatalf("RequireDSN() error = %v", err)
		}
		if got != dsn {
			t.Fatalf("RequireDSN() = %q, want %q", got, dsn)
		}
	})
}
