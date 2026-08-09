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
		"METRICS_TOKEN",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", testDatabaseURL)
	t.Setenv("JWT_SECRET", testJWTSecret)
	t.Setenv("METRICS_TOKEN", "test-metrics-token")
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

func TestLoadRejectsInvalidTrustedProxy(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HTTP_TRUSTED_PROXIES", "not-an-ip")

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), "HTTP_TRUSTED_PROXIES contains invalid IP or CIDR") {
		t.Fatalf("Load() error = %v, want invalid trusted proxy error", err)
	}
}

func TestLoadRejectsAllAddressTrustedProxy(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("HTTP_TRUSTED_PROXIES", "0.0.0.0/0")

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), "must not allow all addresses") {
		t.Fatalf("Load() error = %v, want broad trusted proxy error", err)
	}
}

func TestLoadRejectsInvalidInt64(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("MAX_REQUEST_BODY_BYTES", "not-a-number")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_REQUEST_BODY_BYTES must be an integer") {
		t.Fatalf("expected int64 error, got %v", err)
	}
}

func TestLoadRejectsInvalidValidationBranches(t *testing.T) {
	t.Run("empty HTTP_ADDRESS in struct", func(t *testing.T) {
		cfg := Config{}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "HTTP_ADDRESS must not be empty") {
			t.Fatalf("expected HTTP_ADDRESS error, got %v", err)
		}
	})

	t.Run("empty METRICS_TOKEN in struct", func(t *testing.T) {
		cfg := Config{HTTP: HTTPConfig{Address: ":8080", MaxRequestBodyBytes: 1024}}
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "METRICS_TOKEN must not be empty") {
			t.Fatalf("expected METRICS_TOKEN error, got %v", err)
		}
	})

	t.Run("invalid audit critical age", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("AUDIT_WARNING_AGE", "10m")
		t.Setenv("AUDIT_CRITICAL_AGE", "5m")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "audit duration configuration is invalid") {
			t.Fatalf("expected audit error, got %v", err)
		}
	})

	t.Run("invalid access token TTL", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("ACCESS_TOKEN_TTL", "-1m")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "must be a positive duration") {
			t.Fatalf("expected TTL error, got %v", err)
		}
	})

	t.Run("invalid database URL scheme", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("DATABASE_URL", "http://localhost:5432/db")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "DATABASE_URL must be a valid PostgreSQL URL") {
			t.Fatalf("expected database URL scheme error, got %v", err)
		}
	})
}
