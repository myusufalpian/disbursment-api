package config

import (
	"strings"
	"testing"
	"time"
)

const testDatabaseURL = "postgres://test_user:test_password@localhost:5432/disbursement_test?sslmode=disable"
const testJWTSecret = "test-only-jwt-secret"

func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		want    string
	}{
		{name: "database URL", envName: "DATABASE_URL", want: "DATABASE_URL is required"},
		{name: "JWT secret", envName: "JWT_SECRET", want: "JWT_SECRET is required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.envName, "")

			_, err := Load()

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadUsesApprovedDefaults(t *testing.T) {
	setValidEnvironment(t)

	configuration, err := Load()

	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.HTTP.Address != defaultHTTPAddress {
		t.Errorf("HTTP.Address = %q, want %q", configuration.HTTP.Address, defaultHTTPAddress)
	}
	if configuration.HTTP.MaxRequestBodyBytes != defaultMaxRequestBodyBytes {
		t.Errorf("HTTP.MaxRequestBodyBytes = %d, want %d", configuration.HTTP.MaxRequestBodyBytes, defaultMaxRequestBodyBytes)
	}
	if configuration.Database.MaxOpenConnections != defaultDBMaxOpenConnections {
		t.Errorf("Database.MaxOpenConnections = %d, want %d", configuration.Database.MaxOpenConnections, defaultDBMaxOpenConnections)
	}
	if configuration.Security.AccessTokenTTL != defaultAccessTokenTTL {
		t.Errorf("Security.AccessTokenTTL = %s, want %s", configuration.Security.AccessTokenTTL, defaultAccessTokenTTL)
	}
	if configuration.Audit.CriticalAge != defaultAuditCriticalAge {
		t.Errorf("Audit.CriticalAge = %s, want %s", configuration.Audit.CriticalAge, defaultAuditCriticalAge)
	}
}

func TestLoadRejectsInvalidOptionalValues(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		value   string
		want    string
	}{
		{name: "malformed duration", envName: "HTTP_READ_TIMEOUT", value: "tomorrow", want: "HTTP_READ_TIMEOUT must be a positive duration"},
		{name: "negative open connections", envName: "DB_MAX_OPEN_CONNS", value: "-1", want: "DB_MAX_OPEN_CONNS must be an integer greater than or equal to 1"},
		{name: "idle connections exceed open connections", envName: "DB_MAX_IDLE_CONNS", value: "21", want: "database pool limits are invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnvironment(t)
			t.Setenv(test.envName, test.value)

			_, err := Load()

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HTTP_ADDRESS",
		"HTTP_READ_TIMEOUT",
		"HTTP_WRITE_TIMEOUT",
		"HTTP_IDLE_TIMEOUT",
		"SHUTDOWN_TIMEOUT",
		"MAX_REQUEST_BODY_BYTES",
		"DB_MAX_OPEN_CONNS",
		"DB_MAX_IDLE_CONNS",
		"DB_CONN_MAX_LIFETIME",
		"ACCESS_TOKEN_TTL",
		"REFRESH_TOKEN_TTL",
		"IDEMPOTENCY_LEASE_TTL",
		"IDEMPOTENCY_REPLAY_TTL",
		"AUDIT_OUTBOX_RETENTION",
		"AUDIT_WARNING_AGE",
		"AUDIT_CRITICAL_AGE",
		"AUDIT_RECONCILIATION_INTERVAL",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", testDatabaseURL)
	t.Setenv("JWT_SECRET", testJWTSecret)
}

func TestLoadAllowsValidDurationOverride(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HTTP_READ_TIMEOUT", "25s")

	configuration, err := Load()

	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if configuration.HTTP.ReadTimeout != 25*time.Second {
		t.Errorf("HTTP.ReadTimeout = %s, want 25s", configuration.HTTP.ReadTimeout)
	}
}
