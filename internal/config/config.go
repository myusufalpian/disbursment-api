package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddress             = ":8080"
	defaultDBMaxOpenConnections    = 20
	defaultDBMaxIdleConnections    = 10
	defaultDBConnectionMaxLifetime = 30 * time.Minute
	defaultHTTPReadTimeout         = 10 * time.Second
	defaultHTTPWriteTimeout        = 15 * time.Second
	defaultHTTPIdleTimeout         = 60 * time.Second
	defaultShutdownTimeout         = 10 * time.Second
	defaultAccessTokenTTL          = 15 * time.Minute
	defaultRefreshTokenTTL         = 7 * 24 * time.Hour
	defaultIdempotencyLeaseTTL     = 30 * time.Second
	defaultIdempotencyReplayTTL    = 24 * time.Hour
	defaultAuditOutboxRetention    = 30 * 24 * time.Hour
	defaultAuditWarningAge         = 5 * time.Minute
	defaultAuditCriticalAge        = 15 * time.Minute
	defaultAuditReconciliation     = 15 * time.Minute
	defaultAuditOutboxBatchSize    = 50
	defaultAuditRelayInterval      = 5 * time.Second
	defaultMaxRequestBodyBytes     = 1 << 20
)

type Config struct {
	HTTP        HTTPConfig
	Database    DatabaseConfig
	Security    SecurityConfig
	Idempotency IdempotencyConfig
	Audit       AuditConfig
}

type HTTPConfig struct {
	Address             string
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	ShutdownTimeout     time.Duration
	MaxRequestBodyBytes int64
	MetricsToken        string
	TrustedProxies      []string
}

type DatabaseConfig struct {
	URL                   string
	MaxOpenConnections    int
	MaxIdleConnections    int
	ConnectionMaxLifetime time.Duration
}

type SecurityConfig struct {
	JWTSecret       string
	JWTKeyID        string
	JWTLegacyKeys   map[string]string
	JWTIssuer       string
	JWTAudience     string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type IdempotencyConfig struct {
	LeaseTTL  time.Duration
	ReplayTTL time.Duration
}

type AuditConfig struct {
	OutboxRetention        time.Duration
	WarningAge             time.Duration
	CriticalAge            time.Duration
	ReconciliationInterval time.Duration
	OutboxBatchSize        int
	RelayInterval          time.Duration
}

func Load() (Config, error) {
	databaseURL, err := DatabaseURL()
	if err != nil {
		return Config{}, err
	}
	jwtSecret, err := required("JWT_SECRET")
	if err != nil {
		return Config{}, err
	}
	metricsToken, err := required("METRICS_TOKEN")
	if err != nil {
		return Config{}, err
	}

	var trustedProxies []string
	rawProxies := stringValue("HTTP_TRUSTED_PROXIES", stringValue("TRUSTED_PROXIES", ""))
	if rawProxies != "" {
		for _, p := range strings.Split(rawProxies, ",") {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				trustedProxies = append(trustedProxies, trimmed)
			}
		}
	}

	activeKeyID := stringValue("JWT_KEY_ID", "v1")

	legacyKeys := make(map[string]string)
	if rawLegacy := stringValue("JWT_LEGACY_KEYS", ""); rawLegacy != "" {
		for _, pair := range strings.Split(rawLegacy, ",") {
			parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return Config{}, fmt.Errorf("JWT_LEGACY_KEYS contains malformed pair: %q", pair)
			}
			if parts[0] == activeKeyID {
				return Config{}, fmt.Errorf("JWT_LEGACY_KEYS contains active key ID %q", activeKeyID)
			}
			legacyKeys[parts[0]] = parts[1]
		}
	}

	config := Config{
		HTTP: HTTPConfig{
			Address:        stringValue("HTTP_ADDRESS", defaultHTTPAddress),
			MetricsToken:   metricsToken,
			TrustedProxies: trustedProxies,
		},
		Database: DatabaseConfig{URL: databaseURL},
		Security: SecurityConfig{
			JWTSecret:     jwtSecret,
			JWTKeyID:      activeKeyID,
			JWTLegacyKeys: legacyKeys,
			JWTIssuer:     stringValue("JWT_ISSUER", "disbursement-api"),
			JWTAudience:   stringValue("JWT_AUDIENCE", "disbursement-api-users"),
		},
	}
	if config.HTTP.ReadTimeout, err = readDuration("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout); err != nil {
		return Config{}, err
	}
	if config.HTTP.WriteTimeout, err = readDuration("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout); err != nil {
		return Config{}, err
	}
	if config.HTTP.IdleTimeout, err = readDuration("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout); err != nil {
		return Config{}, err
	}
	if config.HTTP.ShutdownTimeout, err = readDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if config.HTTP.MaxRequestBodyBytes, err = readInt64("MAX_REQUEST_BODY_BYTES", defaultMaxRequestBodyBytes, 1); err != nil {
		return Config{}, err
	}
	if config.Database.MaxOpenConnections, err = readInt("DB_MAX_OPEN_CONNS", defaultDBMaxOpenConnections, 1); err != nil {
		return Config{}, err
	}
	if config.Database.MaxIdleConnections, err = readInt("DB_MAX_IDLE_CONNS", defaultDBMaxIdleConnections, 0); err != nil {
		return Config{}, err
	}
	if config.Database.ConnectionMaxLifetime, err = readDuration("DB_CONN_MAX_LIFETIME", defaultDBConnectionMaxLifetime); err != nil {
		return Config{}, err
	}
	if config.Security.AccessTokenTTL, err = readDuration("ACCESS_TOKEN_TTL", defaultAccessTokenTTL); err != nil {
		return Config{}, err
	}
	if config.Security.RefreshTokenTTL, err = readDuration("REFRESH_TOKEN_TTL", defaultRefreshTokenTTL); err != nil {
		return Config{}, err
	}
	if config.Idempotency.LeaseTTL, err = readDuration("IDEMPOTENCY_LEASE_TTL", defaultIdempotencyLeaseTTL); err != nil {
		return Config{}, err
	}
	if config.Idempotency.ReplayTTL, err = readDuration("IDEMPOTENCY_REPLAY_TTL", defaultIdempotencyReplayTTL); err != nil {
		return Config{}, err
	}
	if config.Audit.OutboxRetention, err = readDuration("AUDIT_OUTBOX_RETENTION", defaultAuditOutboxRetention); err != nil {
		return Config{}, err
	}
	if config.Audit.WarningAge, err = readDuration("AUDIT_WARNING_AGE", defaultAuditWarningAge); err != nil {
		return Config{}, err
	}
	if config.Audit.CriticalAge, err = readDuration("AUDIT_CRITICAL_AGE", defaultAuditCriticalAge); err != nil {
		return Config{}, err
	}
	if config.Audit.ReconciliationInterval, err = readDuration("AUDIT_RECONCILIATION_INTERVAL", defaultAuditReconciliation); err != nil {
		return Config{}, err
	}
	if config.Audit.OutboxBatchSize, err = readInt("AUDIT_OUTBOX_BATCH_SIZE", defaultAuditOutboxBatchSize, 1); err != nil {
		return Config{}, err
	}
	if config.Audit.RelayInterval, err = readDuration("AUDIT_RELAY_INTERVAL", defaultAuditRelayInterval); err != nil {
		return Config{}, err
	}
	if err := config.validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func readDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}

func readInt(name string, fallback, minimum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s must be an integer greater than or equal to %d", name, minimum)
	}
	return parsed, nil
}

func readInt64(name string, fallback, minimum int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s must be an integer greater than or equal to %d", name, minimum)
	}
	return parsed, nil
}

func DatabaseURL() (string, error) {
	rawURL, err := required("DATABASE_URL")
	if err != nil {
		return "", err
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL")
	}
	return rawURL, nil
}

const MinimumSecretLength = 32

func (c Config) ValidateSecretStrength() error {
	if len(c.Security.JWTSecret) < MinimumSecretLength {
		return fmt.Errorf("JWT_SECRET must be at least %d characters", MinimumSecretLength)
	}
	for keyID, secret := range c.Security.JWTLegacyKeys {
		if len(secret) < MinimumSecretLength {
			return fmt.Errorf("JWT_LEGACY_KEYS secret for key ID %q must be at least %d characters", keyID, MinimumSecretLength)
		}
	}
	if len(c.HTTP.MetricsToken) < MinimumSecretLength {
		return fmt.Errorf("METRICS_TOKEN must be at least %d characters", MinimumSecretLength)
	}
	return nil
}

func (c Config) validate() error {
	if c.HTTP.Address == "" {
		return fmt.Errorf("HTTP_ADDRESS must not be empty")
	}
	if c.HTTP.MaxRequestBodyBytes <= 0 {
		return fmt.Errorf("MAX_REQUEST_BODY_BYTES must be greater than zero")
	}
	if strings.TrimSpace(c.HTTP.MetricsToken) == "" {
		return fmt.Errorf("METRICS_TOKEN must not be empty")
	}
	for _, proxy := range c.HTTP.TrustedProxies {
		if err := validateTrustedProxy(proxy); err != nil {
			return err
		}
	}
	if c.Database.MaxOpenConnections <= 0 || c.Database.MaxIdleConnections < 0 || c.Database.MaxIdleConnections > c.Database.MaxOpenConnections {
		return fmt.Errorf("database pool limits are invalid")
	}
	if c.Security.AccessTokenTTL <= 0 || c.Security.RefreshTokenTTL <= 0 || c.Idempotency.LeaseTTL <= 0 || c.Idempotency.ReplayTTL <= 0 {
		return fmt.Errorf("token and idempotency durations must be greater than zero")
	}
	if c.Audit.OutboxRetention <= 0 || c.Audit.WarningAge <= 0 || c.Audit.CriticalAge <= c.Audit.WarningAge || c.Audit.ReconciliationInterval <= 0 {
		return fmt.Errorf("audit duration configuration is invalid")
	}
	return nil
}

func validateTrustedProxy(proxy string) error {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return fmt.Errorf("HTTP_TRUSTED_PROXIES must not contain empty values")
	}
	if ip := net.ParseIP(proxy); ip != nil {
		return nil
	}
	_, network, err := net.ParseCIDR(proxy)
	if err != nil {
		return fmt.Errorf("HTTP_TRUSTED_PROXIES contains invalid IP or CIDR %q", proxy)
	}
	ones, bits := network.Mask.Size()
	if ones == 0 && bits > 0 {
		return fmt.Errorf("HTTP_TRUSTED_PROXIES must not allow all addresses: %q", proxy)
	}
	return nil
}

func required(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func stringValue(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}
