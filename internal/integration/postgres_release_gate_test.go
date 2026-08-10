package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"disbursment-api/internal/config"
	"disbursment-api/internal/database"
	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/migration"
	"disbursment-api/internal/repository"
	postgresrepo "disbursment-api/internal/repository/postgres"
	"disbursment-api/internal/service/auth"
	"disbursment-api/internal/service/disbursement"
	"disbursment-api/internal/service/idempotency"

	migratelib "github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const (
	migrationUpAction   = "up"
	migrationDownAction = "down"

	releaseGateMigrationMetadataTable = "schema_migrations"

	localReleaseGateEnv   = "POSTGRES_RELEASE_GATE"
	localReleaseGateValue = "1"
	githubActionsEnv      = "GITHUB_ACTIONS"
	githubActionsValue    = "true"

	ciDatabaseHost     = "localhost"
	ciDatabasePort     = "5432"
	ciDatabaseUser     = "postgres"
	ciDatabasePassword = "postgres"
	ciDatabaseName     = "disbursement_api"
	ciDatabaseSSLMode  = "disable"

	localReleaseGateDatabaseName = "disbursement_api_release_gate_test"

	releaseGateOpenTimeout = 10 * time.Second
	releaseGateTestTimeout = 15 * time.Second
)

func TestReleaseGateDSNAuthorizationAndConfiguration(t *testing.T) {
	tests := []struct {
		name           string
		environment    map[string]string
		wantDSN        string
		wantConfigured bool
		wantErr        string
	}{
		{
			name: "non-authorized local environment skips",
		},
		{
			name: "authorized local environment requires database configuration",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
			},
			wantErr: "database configuration is required when the PostgreSQL release gate is authorized",
		},
		{
			name: "authorized local environment rejects arbitrary database URL",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DATABASE_URL":      "postgres://postgres:postgres@localhost:5432/disbursement_api?sslmode=disable",
			},
			wantErr: "DATABASE_URL must target the dedicated local PostgreSQL release-gate database",
		},
		{
			name: "authorized local environment accepts dedicated database URL",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DATABASE_URL":      "postgres://postgres:postgres@localhost:5432/disbursement_api_release_gate_test?sslmode=disable",
			},
			wantDSN:        "postgres://postgres:postgres@localhost:5432/disbursement_api_release_gate_test?sslmode=disable",
			wantConfigured: true,
		},
		{
			name: "authorized local environment rejects remote database URL",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DATABASE_URL":      "postgres://postgres:postgres@db.example.test:5432/disbursement_api_release_gate_test?sslmode=disable",
			},
			wantErr: "DATABASE_URL must target a loopback PostgreSQL host",
		},
		{
			name: "authorized local environment rejects arbitrary database variable name",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DB_HOST":           ciDatabaseHost,
				"DB_PORT":           ciDatabasePort,
				"DB_USER":           ciDatabaseUser,
				"DB_PASSWORD":       ciDatabasePassword,
				"DB_NAME":           ciDatabaseName,
				"DB_SSLMODE":        ciDatabaseSSLMode,
			},
			wantErr: "DB_NAME must target the dedicated local PostgreSQL release-gate database",
		},
		{
			name: "authorized local environment accepts dedicated database variable name",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DB_HOST":           ciDatabaseHost,
				"DB_PORT":           ciDatabasePort,
				"DB_USER":           ciDatabaseUser,
				"DB_PASSWORD":       ciDatabasePassword,
				"DB_NAME":           localReleaseGateDatabaseName,
				"DB_SSLMODE":        ciDatabaseSSLMode,
			},
			wantDSN:        "postgres://postgres:postgres@localhost:5432/disbursement_api_release_gate_test?sslmode=disable",
			wantConfigured: true,
		},
		{
			name: "authorized local environment rejects remote database variable host",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DB_HOST":           "db.example.test",
				"DB_PORT":           ciDatabasePort,
				"DB_USER":           ciDatabaseUser,
				"DB_PASSWORD":       ciDatabasePassword,
				"DB_NAME":           localReleaseGateDatabaseName,
				"DB_SSLMODE":        ciDatabaseSSLMode,
			},
			wantErr: "DB_HOST must target a loopback PostgreSQL host",
		},
		{
			name: "authorized GitHub Actions environment requires database configuration",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
			},
			wantErr: "database configuration is required when the PostgreSQL release gate is authorized",
		},
		{
			name: "authorized local environment rejects partial database configuration",
			environment: map[string]string{
				localReleaseGateEnv: localReleaseGateValue,
				"DB_HOST":           ciDatabaseHost,
			},
			wantErr: "all DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, and DB_SSLMODE variables are required",
		},
		{
			name: "authorized GitHub Actions environment rejects partial database configuration",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
			},
			wantErr: "all DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, and DB_SSLMODE variables are required",
		},
		{
			name: "authorized GitHub Actions environment rejects supplied database URL",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DATABASE_URL":   "postgres://postgres:postgres@localhost:5432/disbursement_api?sslmode=disable",
			},
			wantErr: "DATABASE_URL is not permitted for the GitHub Actions release gate",
		},
		{
			name: "authorized GitHub Actions environment derives CI database URL",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantDSN:        "postgres://postgres:postgres@localhost:5432/disbursement_api?sslmode=disable",
			wantConfigured: true,
		},
		{
			name: "authorized GitHub Actions environment rejects wrong CI database host",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        "db.example.test",
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantErr: "DB_HOST must target the GitHub Actions disposable PostgreSQL service",
		},
		{
			name: "authorized GitHub Actions environment rejects wrong CI database port",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        "5433",
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantErr: "DB_PORT must target the GitHub Actions disposable PostgreSQL service",
		},
		{
			name: "authorized GitHub Actions environment rejects wrong CI database name",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        "wrong_database",
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantErr: "DB_NAME must target the GitHub Actions disposable PostgreSQL service",
		},
		{
			name: "authorized GitHub Actions environment rejects mismatched CI user",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        "release_gate",
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantErr: "DB_USER must target the GitHub Actions disposable PostgreSQL service",
		},
		{
			name: "authorized GitHub Actions environment rejects mismatched CI password",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    "not-postgres",
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     ciDatabaseSSLMode,
			},
			wantErr: "DB_PASSWORD must target the GitHub Actions disposable PostgreSQL service",
		},
		{
			name: "authorized GitHub Actions environment rejects mismatched CI SSL mode",
			environment: map[string]string{
				githubActionsEnv: githubActionsValue,
				"DB_HOST":        ciDatabaseHost,
				"DB_PORT":        ciDatabasePort,
				"DB_USER":        ciDatabaseUser,
				"DB_PASSWORD":    ciDatabasePassword,
				"DB_NAME":        ciDatabaseName,
				"DB_SSLMODE":     "require",
			},
			wantErr: "DB_SSLMODE must target the GitHub Actions disposable PostgreSQL service",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setReleaseGateEnvironment(t, test.environment)

			dsn, configured, err := releaseGateDSN()
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("releaseGateDSN() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("releaseGateDSN() error = %v", err)
			}
			if configured != test.wantConfigured {
				t.Fatalf("releaseGateDSN() configured = %t, want %t", configured, test.wantConfigured)
			}
			if dsn != test.wantDSN {
				t.Fatalf("releaseGateDSN() DSN = %q, want %q", dsn, test.wantDSN)
			}
		})
	}
}

func TestReleaseGateLocalHostSafety(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want bool
	}{
		{
			name: "localhost",
			dsn:  "postgres://postgres:postgres@localhost:5432/disbursement_api_release_gate_test?sslmode=disable",
			want: true,
		},
		{
			name: "IPv4 loopback",
			dsn:  "postgres://postgres:postgres@127.0.0.1:5432/disbursement_api_release_gate_test?sslmode=disable",
			want: true,
		},
		{
			name: "IPv6 loopback",
			dsn:  "postgres://postgres:postgres@[::1]:5432/disbursement_api_release_gate_test?sslmode=disable",
			want: true,
		},
		{
			name: "private network address",
			dsn:  "postgres://postgres:postgres@192.0.2.10:5432/disbursement_api_release_gate_test?sslmode=disable",
			want: false,
		},
		{
			name: "remote hostname",
			dsn:  "postgres://postgres:postgres@db.example.test:5432/disbursement_api_release_gate_test?sslmode=disable",
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLocalReleaseGateDSN(test.dsn)
			if test.want && err != nil {
				t.Fatalf("validateLocalReleaseGateDSN() error = %v, want nil", err)
			}
			if !test.want && (err == nil || err.Error() != "DATABASE_URL must target a loopback PostgreSQL host") {
				t.Fatalf("validateLocalReleaseGateDSN() error = %v, want exact loopback-host error", err)
			}
		})
	}
}

func TestMigrationSeedTargetRequiresExplicitAuthorizationAndLoopback(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		authorized bool
		wantErr    string
	}{
		{
			name:    "authorization is required even for loopback",
			dsn:     "postgres://postgres:postgres@localhost:5432/disbursement_api_release_gate_test",
			wantErr: "seeding fixed-credential accounts requires ALLOW_LOCAL_SEED=1",
		},
		{
			name:       "authorized localhost with trailing dot",
			dsn:        "postgres://postgres:postgres@LOCALHOST.:5432/disbursement_api_release_gate_test",
			authorized: true,
		},
		{
			name:       "authorized IPv4 loopback",
			dsn:        "postgres://postgres:postgres@127.0.0.1:5432/disbursement_api_release_gate_test",
			authorized: true,
		},
		{
			name:       "authorized IPv6 loopback",
			dsn:        "postgres://postgres:postgres@[::1]:5432/disbursement_api_release_gate_test",
			authorized: true,
		},
		{
			name:       "authorized remote host is rejected",
			dsn:        "postgres://postgres:postgres@db.example.test:5432/disbursement_api_release_gate_test",
			authorized: true,
			wantErr:    "seed target must be a loopback database host, got \"db.example.test\"",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Passing authorization explicitly prevents inherited environment from changing the result.
			err := migration.AssertLocalSeedTarget(test.dsn, test.authorized)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("AssertLocalSeedTarget() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("AssertLocalSeedTarget() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestReleaseGateMigrationCleanupDecision(t *testing.T) {
	version := 1
	tests := []struct {
		name             string
		state            releaseGateMigrationState
		wantCleanup      bool
		wantForceVersion *int
	}{
		{
			name:  "metadata table absent",
			state: releaseGateMigrationState{},
		},
		{
			name:  "metadata table empty",
			state: releaseGateMigrationState{metadataTablePresent: true},
		},
		{
			name:             "clean migration version",
			state:            releaseGateMigrationState{metadataTablePresent: true, versionPresent: true, version: version},
			wantCleanup:      true,
			wantForceVersion: nil,
		},
		{
			name:             "dirty migration version",
			state:            releaseGateMigrationState{metadataTablePresent: true, versionPresent: true, version: version, dirty: true},
			wantCleanup:      true,
			wantForceVersion: &version,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := releaseGateMigrationCleanupDecision(test.state)
			if decision.cleanup != test.wantCleanup {
				t.Fatalf("cleanup = %t, want %t", decision.cleanup, test.wantCleanup)
			}
			if (decision.forceVersion == nil) != (test.wantForceVersion == nil) {
				t.Fatalf("forceVersion = %v, want %v", decision.forceVersion, test.wantForceVersion)
			}
			if decision.forceVersion != nil && *decision.forceVersion != *test.wantForceVersion {
				t.Fatalf("forceVersion = %d, want %d", *decision.forceVersion, *test.wantForceVersion)
			}
		})
	}
}

func setReleaseGateEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	names := []string{
		"DATABASE_URL",
		localReleaseGateEnv,
		githubActionsEnv,
		"DB_HOST",
		"DB_PORT",
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"DB_SSLMODE",
	}
	previous := make(map[string]string, len(names))
	present := make(map[string]bool, len(names))
	for _, name := range names {
		previous[name], present[name] = os.LookupEnv(name)
		value, configured := values[name]
		var err error
		if configured {
			err = os.Setenv(name, value)
		} else {
			err = os.Unsetenv(name)
		}
		if err != nil {
			t.Fatalf("configure %s for test: %v", name, err)
		}
	}
	t.Cleanup(func() {
		for _, name := range names {
			var err error
			if present[name] {
				err = os.Setenv(name, previous[name])
			} else {
				err = os.Unsetenv(name)
			}
			if err != nil {
				t.Errorf("restore %s after test: %v", name, err)
			}
		}
	})
}

type postgresHarness struct {
	dsn          string
	migrationDir string
	database     *sqlx.DB
}

func TestPostgreSQLReleaseGate_MigrationUpDownUp(t *testing.T) {
	harness := newPostgresHarness(t)

	harness.runMigration(t, migrationUpAction)
	assertTableExists(t, harness.database, "users", true)

	harness.runMigration(t, migrationDownAction)
	assertTableExists(t, harness.database, "users", false)

	harness.runMigration(t, migrationUpAction)
	assertTableExists(t, harness.database, "users", true)
}

type serviceCreateAttempt struct {
	result disbursement.CreateResult
	err    error
}

func TestPostgreSQLReleaseGate_ServiceCreateHasSingleWinnerAndOneOutboxEvent(t *testing.T) {
	harness := newPostgresHarness(t)
	harness.runMigration(t, migrationUpAction)

	ctx, cancel := context.WithTimeout(context.Background(), releaseGateTestTimeout)
	defer cancel()

	actorID := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	requestIDs := []uuid.UUID{
		uuid.MustParse("30000000-0000-4000-8000-000000000011"),
		uuid.MustParse("30000000-0000-4000-8000-000000000012"),
		uuid.MustParse("30000000-0000-4000-8000-000000000013"),
		uuid.MustParse("30000000-0000-4000-8000-000000000014"),
		uuid.MustParse("30000000-0000-4000-8000-000000000015"),
		uuid.MustParse("30000000-0000-4000-8000-000000000016"),
		uuid.MustParse("30000000-0000-4000-8000-000000000017"),
		uuid.MustParse("30000000-0000-4000-8000-000000000018"),
	}
	if _, err := harness.database.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, role)
VALUES ($1, $2, $3, $4)`, actorID, "release_gate_create_user", "unused", "OPERATOR"); err != nil {
		t.Fatalf("insert create actor: %v", err)
	}

	coordinator, err := idempotency.NewDefaultCoordinator(
		postgresrepo.NewIdempotencyStore(harness.database),
		30*time.Second,
		24*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("create idempotency coordinator: %v", err)
	}
	service, err := disbursement.NewService(
		postgresrepo.NewDisbursementStore(harness.database),
		postgresrepo.NewAuditOutboxStore(harness.database),
		postgresrepo.NewTransactor(harness.database),
		coordinator,
		nil,
	)
	if err != nil {
		t.Fatalf("create disbursement service: %v", err)
	}

	input := domain.CreateDisbursementInput{
		RecipientName: "N-way Create Recipient",
		AccountNumber: "1234567890",
		BankCode:      "BCA",
		Amount:        100000,
		Note:          "release-gate create",
	}
	idempotencyKey := uuid.MustParse("30000000-0000-4000-8000-000000000101").String()
	start := make(chan struct{})
	ready := make(chan struct{}, len(requestIDs))
	results := make(chan serviceCreateAttempt, len(requestIDs))
	for _, requestID := range requestIDs {
		requestID := requestID
		go func() {
			ready <- struct{}{}
			<-start
			result, err := service.Create(ctx, actorID, requestID, idempotencyKey, input)
			results <- serviceCreateAttempt{result: result, err: err}
		}()
	}

	for range requestIDs {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("wait for create attempts to reach barrier: %v", ctx.Err())
		}
	}
	close(start)

	attempts := make([]serviceCreateAttempt, 0, len(requestIDs))
	for range requestIDs {
		select {
		case attempt := <-results:
			attempts = append(attempts, attempt)
		case <-ctx.Done():
			t.Fatalf("wait for create attempts to finish: %v", ctx.Err())
		}
	}

	created := 0
	replayed := 0
	inProgress := 0
	for _, attempt := range attempts {
		if attempt.err == nil {
			if attempt.result.IsReplay {
				replayed++
			} else {
				created++
			}
			continue
		}
		domainErr, ok := attempt.err.(*domain.Error)
		if !ok || domainErr.Code != domain.CodeIdempotencyInProgress {
			t.Errorf("unexpected concurrent create error: %v", attempt.err)
			continue
		}
		inProgress++
	}
	if created != 1 {
		t.Fatalf("successful creates = %d, want 1", created)
	}
	if created+replayed+inProgress != len(requestIDs) {
		t.Fatalf("create outcomes = created:%d replayed:%d in_progress:%d, want %d total", created, replayed, inProgress, len(requestIDs))
	}

	var persistedState domain.IdempotencyState
	var persistedDisbursementID uuid.UUID
	var persistedResponseStatus int
	if err := harness.database.QueryRowxContext(ctx, `
SELECT state, disbursement_id, response_status
FROM idempotency_keys
WHERE user_id = $1 AND endpoint = $2 AND key = $3`,
		actorID, "/disbursements", uuid.MustParse(idempotencyKey),
	).Scan(&persistedState, &persistedDisbursementID, &persistedResponseStatus); err != nil {
		t.Fatalf("read persisted completed idempotency key: %v", err)
	}
	if persistedState != domain.IdempotencyCompleted {
		t.Fatalf("persisted idempotency state = %q, want %q", persistedState, domain.IdempotencyCompleted)
	}
	if persistedResponseStatus != 201 {
		t.Fatalf("persisted response status = %d, want 201", persistedResponseStatus)
	}

	var disbursementCount int
	if err := harness.database.GetContext(ctx, &disbursementCount, `
SELECT COUNT(*)
FROM disbursements
WHERE id = $1 AND created_by = $2`, persistedDisbursementID, actorID); err != nil {
		t.Fatalf("count created disbursements: %v", err)
	}
	if disbursementCount != 1 {
		t.Fatalf("persisted disbursement count = %d, want 1", disbursementCount)
	}

	var outboxCount int
	if err := harness.database.GetContext(ctx, &outboxCount, `
SELECT COUNT(*)
FROM audit_outbox
WHERE entity_id = $1 AND action = $2`, persistedDisbursementID, "disbursement.created"); err != nil {
		t.Fatalf("count create outbox events: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("create outbox event count = %d, want 1", outboxCount)
	}
}

func TestPostgreSQLReleaseGate_IdempotencyClaimHasSingleWinner(t *testing.T) {
	harness := newPostgresHarness(t)
	harness.runMigration(t, migrationUpAction)

	database := harness.database

	ctx := context.Background()
	userID := uuid.New()
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, role)
VALUES ($1, $2, $3, $4)`, userID, "release_gate_"+userID.String()[:8], "unused", "ADMIN"); err != nil {
		t.Fatalf("insert release-gate user: %v", err)
	}

	store := postgresrepo.NewIdempotencyStore(database)
	scope := domain.IdempotencyScope{
		UserID:   userID,
		Endpoint: "/api/v1/disbursements",
		Key:      uuid.New(),
	}
	fingerprint := sha256.Sum256([]byte(`{"amount":10000,"recipient_name":"release-gate"}`))
	now := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	requests := []domain.IdempotencyClaimRequest{
		{
			Scope:       scope,
			Fingerprint: fingerprint,
			ClaimID:     uuid.New(),
			LeaseUntil:  now.Add(time.Minute),
			ExpiresAt:   now.Add(24 * time.Hour),
			Now:         now,
		},
		{
			Scope:       scope,
			Fingerprint: fingerprint,
			ClaimID:     uuid.New(),
			LeaseUntil:  now.Add(time.Minute),
			ExpiresAt:   now.Add(24 * time.Hour),
			Now:         now,
		},
	}

	start := make(chan struct{})
	ready := make(chan struct{}, len(requests))
	results := make(chan claimAttempt, len(requests))
	acquireContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, request := range requests {
		go func(request domain.IdempotencyClaimRequest) {
			ready <- struct{}{}
			<-start
			result, err := store.Acquire(acquireContext, request)
			results <- claimAttempt{result: result, err: err}
		}(request)
	}

	for range requests {
		select {
		case <-ready:
		case <-acquireContext.Done():
			t.Fatalf("wait for idempotency claim attempts to reach barrier: %v", acquireContext.Err())
		}
	}
	close(start)

	attempts := make([]claimAttempt, 0, len(requests))
	for range requests {
		attempts = append(attempts, <-results)
	}

	var acquired []domain.IdempotencyClaimResult
	inProgress := 0
	for _, attempt := range attempts {
		if attempt.err != nil {
			t.Errorf("acquire claim: %v", attempt.err)
			continue
		}
		switch attempt.result.Outcome {
		case domain.ClaimAcquired:
			acquired = append(acquired, attempt.result)
		case domain.ClaimInProgress:
			inProgress++
		default:
			t.Errorf("unexpected claim outcome %q", attempt.result.Outcome)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	if len(acquired) != 1 || inProgress != 1 {
		t.Fatalf("expected one acquired claim and one in-progress result, got acquired=%d in_progress=%d", len(acquired), inProgress)
	}

	var storedClaimID uuid.UUID
	var storedState domain.IdempotencyState
	if err := database.QueryRowxContext(ctx, `
SELECT claim_id, state
FROM idempotency_keys
WHERE user_id = $1 AND endpoint = $2 AND key = $3`,
		scope.UserID, scope.Endpoint, scope.Key,
	).Scan(&storedClaimID, &storedState); err != nil {
		t.Fatalf("read persisted idempotency claim: %v", err)
	}
	if storedClaimID != acquired[0].ClaimID {
		t.Fatalf("persisted claim %s does not match winner %s", storedClaimID, acquired[0].ClaimID)
	}
	if storedState != domain.IdempotencyInProgress {
		t.Fatalf("expected persisted claim state %q, got %q", domain.IdempotencyInProgress, storedState)
	}

	if err := store.Release(ctx, scope, acquired[0].ClaimID); err != nil {
		t.Fatalf("release winning claim: %v", err)
	}
	var claimCount int
	if err := database.GetContext(ctx, &claimCount, `
SELECT COUNT(*)
FROM idempotency_keys
WHERE user_id = $1 AND endpoint = $2 AND key = $3`, scope.UserID, scope.Endpoint, scope.Key); err != nil {
		t.Fatalf("count released claims: %v", err)
	}
	if claimCount != 0 {
		t.Fatalf("expected released claim to be removed, found %d rows", claimCount)
	}
}

type serviceFinalizationAttempt struct {
	updated domain.Disbursement
	err     error
}

func TestPostgreSQLReleaseGate_ServiceFinalizationHasSingleWinnerAndOneOutboxEvent(t *testing.T) {
	harness := newPostgresHarness(t)
	harness.runMigration(t, migrationUpAction)
	harness.database.SetMaxOpenConns(32)
	harness.database.SetMaxIdleConns(16)

	ctx, cancel := context.WithTimeout(context.Background(), releaseGateTestTimeout)
	defer cancel()

	actorIDs := []uuid.UUID{
		uuid.MustParse("50000000-0000-4000-8000-000000000001"),
		uuid.MustParse("50000000-0000-4000-8000-000000000002"),
		uuid.MustParse("50000000-0000-4000-8000-000000000003"),
		uuid.MustParse("50000000-0000-4000-8000-000000000004"),
		uuid.MustParse("50000000-0000-4000-8000-000000000005"),
		uuid.MustParse("50000000-0000-4000-8000-000000000006"),
		uuid.MustParse("50000000-0000-4000-8000-000000000007"),
		uuid.MustParse("50000000-0000-4000-8000-000000000008"),
	}
	for index, actorID := range actorIDs {
		if _, err := harness.database.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, role)
VALUES ($1, $2, $3, $4)`, actorID, fmt.Sprintf("release_gate_finalize_%d", index), "unused", "ADMIN"); err != nil {
			t.Fatalf("insert finalization actor %s: %v", actorID, err)
		}
	}

	disbursementID := uuid.MustParse("50000000-0000-4000-8000-000000000101")
	createdAt := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	if _, err := harness.database.ExecContext(ctx, `
INSERT INTO disbursements (
	id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		disbursementID, "N-way Finalization Recipient", "1234567890", "BCA", int64(10000), int64(2500),
		string(domain.StatusPending), "release-gate finalization", actorIDs[0], createdAt,
	); err != nil {
		t.Fatalf("insert pending disbursement: %v", err)
	}

	service, err := disbursement.NewService(
		postgresrepo.NewDisbursementStore(harness.database),
		postgresrepo.NewAuditOutboxStore(harness.database),
		postgresrepo.NewTransactor(harness.database),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create disbursement service: %v", err)
	}

	start := make(chan struct{})
	ready := make(chan struct{}, len(actorIDs))
	results := make(chan serviceFinalizationAttempt, len(actorIDs))
	for index, actorID := range actorIDs {
		actorID := actorID
		requestID := uuid.MustParse(fmt.Sprintf("50000000-0000-4000-8000-%012d", index+201))
		decision := domain.Decision{Status: domain.StatusRejected, Note: "release-gate loser"}
		if index == 0 {
			decision = domain.Decision{Status: domain.StatusApproved, Note: "release-gate winner"}
		}
		go func() {
			ready <- struct{}{}
			<-start
			updated, err := service.UpdateStatus(ctx, actorID, requestID, disbursementID, decision)
			results <- serviceFinalizationAttempt{updated: updated, err: err}
		}()
	}

	for range actorIDs {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("wait for finalization attempts to reach barrier: %v", ctx.Err())
		}
	}
	close(start)

	attempts := make([]serviceFinalizationAttempt, 0, len(actorIDs))
	for range actorIDs {
		select {
		case attempt := <-results:
			attempts = append(attempts, attempt)
		case <-ctx.Done():
			t.Fatalf("wait for finalization attempts to finish: %v", ctx.Err())
		}
	}

	winners := 0
	conflicts := 0
	var winner serviceFinalizationAttempt
	for _, attempt := range attempts {
		if attempt.err == nil {
			winners++
			winner = attempt
			continue
		}
		domainErr := domain.AsError(attempt.err)
		if domainErr.Code != domain.CodeDisbursementAlreadyFinalized {
			t.Errorf("unexpected finalization error: %v", attempt.err)
			continue
		}
		conflicts++
	}
	if winners != 1 || conflicts != len(actorIDs)-1 {
		t.Fatalf("finalization outcomes = winners:%d conflicts:%d, want winners:1 conflicts:%d", winners, conflicts, len(actorIDs)-1)
	}

	var persistedStatus string
	var persistedDecidedBy uuid.UUID
	var persistedDecisionNote string
	if err := harness.database.QueryRowxContext(ctx, `
SELECT status, decided_by, decision_note
FROM disbursements
WHERE id = $1`, disbursementID).Scan(&persistedStatus, &persistedDecidedBy, &persistedDecisionNote); err != nil {
		t.Fatalf("read persisted finalization: %v", err)
	}
	if persistedStatus != string(winner.updated.Status) {
		t.Fatalf("persisted status = %q, winner status = %q", persistedStatus, winner.updated.Status)
	}
	if persistedDecidedBy != winner.updated.DecidedBy {
		t.Fatalf("persisted decided_by = %s, winner decided_by = %s", persistedDecidedBy, winner.updated.DecidedBy)
	}
	if persistedDecisionNote != winner.updated.DecisionNote {
		t.Fatalf("persisted decision_note = %q, winner decision_note = %q", persistedDecisionNote, winner.updated.DecisionNote)
	}

	var outboxCount int
	if err := harness.database.GetContext(ctx, &outboxCount, `
SELECT COUNT(*)
FROM audit_outbox
WHERE entity_id = $1`, disbursementID); err != nil {
		t.Fatalf("count finalization outbox events: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("finalization outbox event count = %d, want 1", outboxCount)
	}
	var outboxAction string
	var outboxActor uuid.UUID
	if err := harness.database.QueryRowxContext(ctx, `
SELECT action, actor_id
FROM audit_outbox
WHERE entity_id = $1`, disbursementID).Scan(&outboxAction, &outboxActor); err != nil {
		t.Fatalf("read finalization outbox event: %v", err)
	}
	wantAction := "disbursement.rejected"
	if winner.updated.Status == domain.StatusApproved {
		wantAction = "disbursement.approved"
	}
	if outboxAction != wantAction || outboxActor != winner.updated.DecidedBy {
		t.Fatalf("outbox event = action:%q actor:%s, want action:%q actor:%s", outboxAction, outboxActor, wantAction, winner.updated.DecidedBy)
	}
}

func TestPostgreSQLReleaseGate_FinalizationHasSingleWinner(t *testing.T) {
	harness := newPostgresHarness(t)
	harness.runMigration(t, migrationUpAction)

	ctx, cancel := context.WithTimeout(context.Background(), releaseGateTestTimeout)
	defer cancel()

	approvedBy := uuid.New()
	rejectedBy := uuid.New()
	createdBy := approvedBy
	for _, userID := range []uuid.UUID{approvedBy, rejectedBy} {
		if _, err := harness.database.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, role)
VALUES ($1, $2, $3, $4)`, userID, "release_gate_"+userID.String()[:8], "unused", "ADMIN"); err != nil {
			t.Fatalf("insert finalization user %s: %v", userID, err)
		}
	}

	disbursementID := uuid.New()
	createdAt := time.Now().UTC()
	if _, err := harness.database.ExecContext(ctx, `
INSERT INTO disbursements (
	id, recipient_name, account_number, bank_code, amount, admin_fee,
	status, note, created_by, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
		disbursementID, "Release Gate Recipient", "1234567890", "BCA", int64(10000), int64(2500),
		string(domain.StatusPending), "race", createdBy, createdAt,
	); err != nil {
		t.Fatalf("insert pending disbursement: %v", err)
	}

	store := postgresrepo.NewDisbursementStore(harness.database)
	transactor := postgresrepo.NewTransactor(harness.database)
	decisions := []finalizationDecision{
		{actorID: approvedBy, status: domain.StatusApproved, note: "approved winner"},
		{actorID: rejectedBy, status: domain.StatusRejected, note: "rejected loser"},
	}
	ready := make(chan struct{}, len(decisions))
	release := make(chan struct{})
	results := make(chan finalizationAttempt, len(decisions))

	for _, decision := range decisions {
		decision := decision
		go func() {
			attempt := finalizationAttempt{}
			attempt.err = transactor.WithinTransaction(ctx, func(ctx context.Context, transaction repository.Transaction) error {
				ready <- struct{}{}
				<-release
				attempt.updated, attempt.err = store.UpdateStatus(ctx, transaction, disbursementID, domain.Decision{
					Status:  decision.status,
					ActorID: decision.actorID,
					Note:    decision.note,
				})
				return attempt.err
			})
			results <- attempt
		}()
	}

	for range decisions {
		select {
		case <-ready:
		case <-ctx.Done():
			close(release)
			t.Fatalf("wait for finalization transactions to start: %v", ctx.Err())
		}
	}
	close(release)

	attempts := make([]finalizationAttempt, 0, len(decisions))
	for range decisions {
		select {
		case attempt := <-results:
			attempts = append(attempts, attempt)
		case <-ctx.Done():
			t.Fatalf("wait for finalization transactions to finish: %v", ctx.Err())
		}
	}

	var winner finalizationAttempt
	winners := 0
	conflicts := 0
	for _, attempt := range attempts {
		if attempt.err == nil {
			winners++
			winner = attempt
			if attempt.updated.ID != disbursementID {
				t.Errorf("winner returned disbursement %s, want %s", attempt.updated.ID, disbursementID)
			}

			continue
		}
		if attempt.updated.ID != uuid.Nil {
			t.Errorf("conflicting transaction returned persisted result %s", attempt.updated.ID)
		}
		var repositoryError *repository.Error
		if !errors.As(attempt.err, &repositoryError) || repositoryError.Category != repository.ErrorConflict {
			t.Errorf("conflicting transaction error = %v, want repository conflict", attempt.err)
			continue
		}
		conflicts++
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one committed winner and one conflict, got winners=%d conflicts=%d", winners, conflicts)
	}

	var persistedStatus string
	var persistedDecidedBy uuid.UUID
	var persistedDecisionNote string
	var persistedDecidedAt time.Time
	if err := harness.database.QueryRowxContext(ctx, `
SELECT status, decided_by, decision_note, decided_at
FROM disbursements
WHERE id = $1`, disbursementID).Scan(
		&persistedStatus, &persistedDecidedBy, &persistedDecisionNote, &persistedDecidedAt,
	); err != nil {
		t.Fatalf("read finalization result: %v", err)
	}
	if persistedStatus != string(winner.updated.Status) {
		t.Fatalf("persisted status = %q, winner result status = %q", persistedStatus, winner.updated.Status)
	}
	if persistedDecidedBy != winner.updated.DecidedBy {
		t.Fatalf("persisted decided_by = %s, winner result decided_by = %s", persistedDecidedBy, winner.updated.DecidedBy)
	}
	if persistedDecisionNote != winner.updated.DecisionNote {
		t.Fatalf("persisted decision_note = %q, winner result decision_note = %q", persistedDecisionNote, winner.updated.DecisionNote)
	}
	if winner.updated.DecidedAt == nil || !persistedDecidedAt.Equal(*winner.updated.DecidedAt) {
		t.Fatalf("persisted decided_at = %s, winner result decided_at = %v", persistedDecidedAt, winner.updated.DecidedAt)
	}
}

func TestPostgreSQLReleaseGate_RefreshRotationHasSingleWinner(t *testing.T) {
	harness := newPostgresHarness(t)
	harness.runMigration(t, migrationUpAction)

	ctx, cancel := context.WithTimeout(context.Background(), releaseGateTestTimeout)
	defer cancel()

	if err := migration.ApplySeed(ctx, harness.database, filepath.Join(harness.migrationDir, "000001_local_users.seed.sql")); err != nil {
		t.Fatalf("seed release-gate users: %v", err)
	}

	// The seed fixture stores password hashes only; create the initial session for its seeded user through the real repository.
	userStore := postgresrepo.NewUserStore(harness.database)
	sessionStore := postgresrepo.NewRefreshSessionStore(harness.database)
	transactor := postgresrepo.NewTransactor(harness.database)
	seededUser, err := userStore.FindByUsername(ctx, "local_test_operator")
	if err != nil {
		t.Fatalf("find seeded release-gate user: %v", err)
	}
	if seededUser.Role != string(domain.RoleOperator) {
		t.Fatalf("seeded release-gate user role = %q, want %q", seededUser.Role, domain.RoleOperator)
	}

	initialRefreshToken := uuid.New().String()
	initialSession := repository.RefreshSession{
		ID:        uuid.New(),
		UserID:    seededUser.ID,
		TokenHash: hashReleaseGateRefreshToken(initialRefreshToken),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := transactor.WithinTransaction(ctx, func(ctx context.Context, transaction repository.Transaction) error {
		return sessionStore.Create(ctx, transaction, initialSession)
	}); err != nil {
		t.Fatalf("create seeded user's refresh session: %v", err)
	}

	authService, err := auth.NewService(
		userStore,
		sessionStore,
		transactor,
		"release-gate-refresh-test-secret",
		15*time.Minute,
		7*24*time.Hour,
		nil,
	)
	if err != nil {
		t.Fatalf("failed to create auth service: %v", err)
	}

	const concurrentAttempts = 2
	ready := make(chan struct{}, concurrentAttempts)
	start := make(chan struct{})
	results := make(chan refreshAttempt, concurrentAttempts)
	for range concurrentAttempts {
		go func() {
			ready <- struct{}{}
			<-start
			result, err := authService.Refresh(ctx, dto.RefreshRequest{RefreshToken: initialRefreshToken})
			results <- refreshAttempt{result: result, err: err}
		}()
	}

	for range concurrentAttempts {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatalf("wait for refresh attempts to reach barrier: %v", ctx.Err())
		}
	}
	close(start)

	attempts := make([]refreshAttempt, 0, concurrentAttempts)
	for range concurrentAttempts {
		select {
		case attempt := <-results:
			attempts = append(attempts, attempt)
		case <-ctx.Done():
			t.Fatalf("wait for refresh attempts to finish: %v", ctx.Err())
		}
	}

	var winner *dto.TokenResponse
	losers := 0
	for _, attempt := range attempts {
		if attempt.err == nil {
			if winner != nil {
				t.Fatal("concurrent refresh produced more than one success")
			}
			if attempt.result == nil || attempt.result.RefreshToken == "" || attempt.result.AccessToken == "" {
				t.Fatalf("successful refresh returned incomplete token response: %+v", attempt.result)
			}
			winner = attempt.result
			continue
		}

		if domain.AsError(attempt.err).Code != domain.CodeInvalidRefreshToken {
			t.Errorf("losing refresh error = %v, want %s", attempt.err, domain.CodeInvalidRefreshToken)
			continue
		}
		losers++
	}
	if winner == nil || losers != 1 {
		t.Fatalf("expected exactly one successful refresh and one INVALID_REFRESH_TOKEN loser, winner=%t losers=%d", winner != nil, losers)
	}

	var sessions []refreshSessionRecord
	if err := harness.database.SelectContext(ctx, &sessions, `
SELECT id, user_id, token_hash, revoked_at, replaced_by_id
FROM refresh_sessions
WHERE user_id = $1
ORDER BY created_at, id`, seededUser.ID); err != nil {
		t.Fatalf("read refresh-session rotation state: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected exactly two persisted sessions after concurrent rotation, got %d", len(sessions))
	}

	oldSession, ok := findRefreshSession(sessions, initialSession.TokenHash)
	if !ok {
		t.Fatalf("persisted refresh sessions do not contain the initial token hash")
	}
	if oldSession.RevokedAt == nil {
		t.Fatal("initial refresh session was not revoked")
	}
	if oldSession.ReplacedByID == nil {
		t.Fatal("initial refresh session has no replacement link")
	}

	winningSession, ok := findRefreshSession(sessions, hashReleaseGateRefreshToken(winner.RefreshToken))
	if !ok {
		t.Fatalf("persisted refresh sessions do not contain the winning token hash")
	}
	if winningSession.RevokedAt != nil {
		t.Fatal("winning replacement session is not active")
	}
	if winningSession.ReplacedByID != nil {
		t.Fatalf("winning replacement session unexpectedly points to %s", *winningSession.ReplacedByID)
	}
	if *oldSession.ReplacedByID != winningSession.ID {
		t.Fatalf("initial refresh session replacement = %s, want winning session %s", *oldSession.ReplacedByID, winningSession.ID)
	}

	var activeReplacementCount int
	if err := harness.database.GetContext(ctx, &activeReplacementCount, `
SELECT COUNT(*)
FROM refresh_sessions AS replacement
JOIN refresh_sessions AS old_session ON old_session.replaced_by_id = replacement.id
WHERE old_session.token_hash = $1
  AND replacement.revoked_at IS NULL`, initialSession.TokenHash); err != nil {
		t.Fatalf("count active refresh-session replacements: %v", err)
	}
	if activeReplacementCount != 1 {
		t.Fatalf("expected exactly one active replacement for the initial session, got %d", activeReplacementCount)
	}

	var orphanedSessionCount int
	if err := harness.database.GetContext(ctx, &orphanedSessionCount, `
SELECT COUNT(*)
FROM refresh_sessions AS session
LEFT JOIN refresh_sessions AS replacement ON replacement.id = session.replaced_by_id
WHERE session.user_id = $1
  AND session.replaced_by_id IS NOT NULL
  AND replacement.id IS NULL`, seededUser.ID); err != nil {
		t.Fatalf("count orphaned refresh-session links: %v", err)
	}
	if orphanedSessionCount != 0 {
		t.Fatalf("expected no orphaned refresh-session links, got %d", orphanedSessionCount)
	}

	var activeLinkedSessionCount int
	if err := harness.database.GetContext(ctx, &activeLinkedSessionCount, `
SELECT COUNT(*)
FROM refresh_sessions
WHERE user_id = $1
  AND revoked_at IS NULL
  AND replaced_by_id IS NOT NULL`, seededUser.ID); err != nil {
		t.Fatalf("count active refresh sessions with replacement links: %v", err)
	}
	if activeLinkedSessionCount != 0 {
		t.Fatalf("expected no active refresh session with a replacement link, got %d", activeLinkedSessionCount)
	}

	replacement, err := authService.Refresh(ctx, dto.RefreshRequest{RefreshToken: winner.RefreshToken})
	if err != nil {
		t.Fatalf("refresh with winning replacement token: %v", err)
	}
	if replacement == nil || replacement.RefreshToken == "" || replacement.AccessToken == "" {
		t.Fatalf("replacement refresh returned incomplete token response: %+v", replacement)
	}
	if replacement.RefreshToken == winner.RefreshToken {
		t.Fatal("replacement refresh token was not rotated")
	}
}

type claimAttempt struct {
	result domain.IdempotencyClaimResult
	err    error
}

type refreshAttempt struct {
	result *dto.TokenResponse
	err    error
}

type refreshSessionRecord struct {
	ID           uuid.UUID  `db:"id"`
	UserID       uuid.UUID  `db:"user_id"`
	TokenHash    string     `db:"token_hash"`
	RevokedAt    *time.Time `db:"revoked_at"`
	ReplacedByID *uuid.UUID `db:"replaced_by_id"`
}

func findRefreshSession(sessions []refreshSessionRecord, tokenHash string) (refreshSessionRecord, bool) {
	for _, session := range sessions {
		if session.TokenHash == tokenHash {
			return session, true
		}
	}
	return refreshSessionRecord{}, false
}

func hashReleaseGateRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type finalizationDecision struct {
	actorID uuid.UUID
	status  domain.DisbursementStatus
	note    string
}

type finalizationAttempt struct {
	updated domain.Disbursement
	err     error
}

type releaseGateMigrationState struct {
	metadataTablePresent bool
	versionPresent       bool
	version              int
	dirty                bool
}

type releaseGateMigrationCleanup struct {
	cleanup      bool
	forceVersion *int
}

func releaseGateMigrationCleanupDecision(state releaseGateMigrationState) releaseGateMigrationCleanup {
	if !state.metadataTablePresent || !state.versionPresent {
		return releaseGateMigrationCleanup{}
	}
	decision := releaseGateMigrationCleanup{cleanup: true}
	if state.dirty {
		version := state.version
		decision.forceVersion = &version
	}
	return decision
}

func cleanupPostgresHarness(harness *postgresHarness) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), releaseGateTestTimeout)
	defer cancel()

	state, err := releaseGateMigrationStateFromDatabase(cleanupContext, harness.database)
	if err != nil {
		return fmt.Errorf("inspect schema_migrations: %w", err)
	}
	decision := releaseGateMigrationCleanupDecision(state)
	if !decision.cleanup {
		return nil
	}
	if decision.forceVersion != nil {
		if err := forceReleaseGateMigration(harness.dsn, harness.migrationDir, *decision.forceVersion); err != nil {
			return fmt.Errorf("reset dirty migration version %d: %w", *decision.forceVersion, err)
		}
	}
	if err := migration.Run(harness.dsn, harness.migrationDir, migrationDownAction, 0); err != nil {
		return fmt.Errorf("run down migrations: %w", err)
	}
	return nil
}

func releaseGateMigrationStateFromDatabase(ctx context.Context, database *sqlx.DB) (releaseGateMigrationState, error) {
	var metadataTablePresent bool
	if err := database.GetContext(ctx, &metadataTablePresent, `
SELECT to_regclass('public.schema_migrations') IS NOT NULL`); err != nil {
		return releaseGateMigrationState{}, err
	}
	if !metadataTablePresent {
		return releaseGateMigrationState{}, nil
	}

	state := releaseGateMigrationState{metadataTablePresent: true}
	row := database.QueryRowxContext(ctx, `
SELECT version, dirty
FROM public.schema_migrations
LIMIT 1`)
	if err := row.Scan(&state.version, &state.dirty); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return state, nil
		}
		return releaseGateMigrationState{}, err
	}
	state.versionPresent = true
	return state, nil
}

func forceReleaseGateMigration(dsn string, migrationDir string, version int) error {
	absoluteDirectory, err := filepath.Abs(migrationDir)
	if err != nil {
		return fmt.Errorf("resolve migration directory: %w", err)
	}
	migrator, err := migratelib.New("file://"+absoluteDirectory, dsn)
	if err != nil {
		return fmt.Errorf("initialize migration reset: %w", err)
	}
	defer migrator.Close()
	if err := migrator.Force(version); err != nil {
		return fmt.Errorf("force migration version: %w", err)
	}
	return nil
}

func newPostgresHarness(t *testing.T) *postgresHarness {
	t.Helper()

	dsn, configured, err := releaseGateDSN()
	if err != nil {
		t.Fatalf("invalid PostgreSQL release-gate configuration: %v", err)
	}
	if !configured {
		t.Skip("PostgreSQL release gates require POSTGRES_RELEASE_GATE=1 locally or the authorized GitHub Actions disposable service")
	}

	migrationDir := releaseGateMigrationDir(t)
	openContext, cancel := context.WithTimeout(context.Background(), releaseGateOpenTimeout)
	defer cancel()
	opened, err := database.Open(openContext, config.DatabaseConfig{
		URL:                   dsn,
		MaxOpenConnections:    4,
		MaxIdleConnections:    4,
		ConnectionMaxLifetime: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("open authorized PostgreSQL release-gate database: %v", err)
	}

	harness := &postgresHarness{
		dsn:          dsn,
		migrationDir: migrationDir,
		database:     opened,
	}
	t.Cleanup(func() {
		if err := cleanupPostgresHarness(harness); err != nil {
			t.Errorf("clean up PostgreSQL release-gate migrations: %v", err)
		}
		if err := harness.database.Close(); err != nil {
			t.Errorf("close PostgreSQL connection: %v", err)
		}
	})

	assertFreshReleaseGateDatabase(t, opened)
	return harness
}

func (harness *postgresHarness) runMigration(t *testing.T, action string) {
	t.Helper()
	if err := migration.Run(harness.dsn, harness.migrationDir, action, 0); err != nil {
		t.Fatalf("run %s migrations: %v", action, err)
	}
}

func TestFreshReleaseGateDatabaseError(t *testing.T) {
	tests := []struct {
		name    string
		objects []releaseGateSchemaObject
		wantErr string
	}{
		{
			name: "empty public schema is accepted",
		},
		{
			name: "golang-migrate metadata table is accepted",
			objects: []releaseGateSchemaObject{
				{Kind: "table", Name: releaseGateMigrationMetadataTable},
			},
		},
		{
			name: "table is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "table", Name: "users"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains 1 table(s)",
		},
		{
			name: "type is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "type", Name: "user_role"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains existing non-table object(s): type user_role",
		},
		{
			name: "function is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "function", Name: "prevent_audit_outbox_event_id_change()"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains existing non-table object(s): function prevent_audit_outbox_event_id_change()",
		},
		{
			name: "trigger is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "trigger", Name: "audit_outbox.audit_outbox_event_id_immutable"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains existing non-table object(s): trigger audit_outbox.audit_outbox_event_id_immutable",
		},
		{
			name: "sequence is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "sequence", Name: "users_id_seq"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains existing non-table object(s): sequence users_id_seq",
		},
		{
			name: "view is rejected",
			objects: []releaseGateSchemaObject{
				{Kind: "view", Name: "active_users"},
			},
			wantErr: "refusing destructive release-gate migrations: public schema contains existing non-table object(s): view active_users",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := freshReleaseGateDatabaseError(test.objects)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("freshReleaseGateDatabaseError() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("freshReleaseGateDatabaseError() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

type releaseGateSchemaObject struct {
	Kind string `db:"object_kind"`
	Name string `db:"object_name"`
}

func freshReleaseGateDatabaseError(objects []releaseGateSchemaObject) error {
	tableCount := 0
	nonTableObjects := make([]string, 0, len(objects))
	for _, object := range objects {
		if object.Kind == "table" && object.Name == releaseGateMigrationMetadataTable {
			continue
		}
		if object.Kind == "table" {
			tableCount++
			continue
		}
		nonTableObjects = append(nonTableObjects, fmt.Sprintf("%s %s", object.Kind, object.Name))
	}
	if tableCount != 0 {
		return fmt.Errorf("refusing destructive release-gate migrations: public schema contains %d table(s)", tableCount)
	}
	if len(nonTableObjects) != 0 {
		return fmt.Errorf("refusing destructive release-gate migrations: public schema contains existing non-table object(s): %s", strings.Join(nonTableObjects, ", "))
	}
	return nil
}

func assertFreshReleaseGateDatabase(t *testing.T, database *sqlx.DB) {
	t.Helper()
	var objects []releaseGateSchemaObject
	if err := database.SelectContext(context.Background(), &objects, `
WITH public_relations AS (
	SELECT c.oid, c.relname, c.relkind
	FROM pg_class AS c
	JOIN pg_namespace AS n ON n.oid = c.relnamespace
	WHERE n.nspname = 'public'
),
non_extension_relations AS (
	SELECT relation.oid, relation.relname, relation.relkind
	FROM public_relations AS relation
	WHERE NOT EXISTS (
		SELECT 1
		FROM pg_depend AS dependency
		WHERE dependency.classid = 'pg_class'::regclass
		  AND dependency.objid = relation.oid
		  AND dependency.deptype = 'e'
	)
),
public_objects AS (
	SELECT 'table' AS object_kind, relation.relname AS object_name
	FROM public_relations AS relation
	WHERE relation.relkind IN ('r', 'p', 'f')
	UNION ALL
	SELECT 'view' AS object_kind, relation.relname AS object_name
	FROM public_relations AS relation
	WHERE relation.relkind IN ('v', 'm')
	UNION ALL
	SELECT 'sequence' AS object_kind, relation.relname AS object_name
	FROM non_extension_relations AS relation
	WHERE relation.relkind = 'S'
	UNION ALL
	SELECT 'type' AS object_kind, type_row.typname AS object_name
	FROM pg_type AS type_row
	JOIN pg_namespace AS namespace_row ON namespace_row.oid = type_row.typnamespace
	WHERE namespace_row.nspname = 'public'
	  AND type_row.typelem = 0
	  AND (to_regclass('public.schema_migrations') IS NULL OR type_row.typrelid <> to_regclass('public.schema_migrations')::oid)
	  AND NOT EXISTS (
		SELECT 1
		FROM pg_depend AS dependency
		WHERE dependency.classid = 'pg_type'::regclass
		  AND dependency.objid = type_row.oid
		  AND dependency.deptype = 'e'
	  )
	UNION ALL
	SELECT 'function' AS object_kind, proc_row.oid::regprocedure::text AS object_name
	FROM pg_proc AS proc_row
	JOIN pg_namespace AS namespace_row ON namespace_row.oid = proc_row.pronamespace
	WHERE namespace_row.nspname = 'public'
	  AND NOT EXISTS (
		SELECT 1
		FROM pg_depend AS dependency
		WHERE dependency.classid = 'pg_proc'::regclass
		  AND dependency.objid = proc_row.oid
		  AND dependency.deptype = 'e'
	  )
	UNION ALL
	SELECT 'trigger' AS object_kind, relation.relname || '.' || trigger_row.tgname AS object_name
	FROM pg_trigger AS trigger_row
	JOIN non_extension_relations AS relation ON relation.oid = trigger_row.tgrelid
	WHERE NOT trigger_row.tgisinternal
	  AND NOT EXISTS (
		SELECT 1
		FROM pg_depend AS dependency
		WHERE dependency.classid = 'pg_trigger'::regclass
		  AND dependency.objid = trigger_row.oid
		  AND dependency.deptype = 'e'
	  )
)
SELECT object_kind, object_name
FROM public_objects
ORDER BY object_kind, object_name`); err != nil {
		t.Fatalf("verify release-gate database is empty: %v", err)
	}
	if err := freshReleaseGateDatabaseError(objects); err != nil {
		t.Fatal(err)
	}
}

func assertTableExists(t *testing.T, database *sqlx.DB, table string, want bool) {
	t.Helper()
	var exists bool
	if err := database.GetContext(context.Background(), &exists, `SELECT to_regclass($1) IS NOT NULL`, "public."+table); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	if exists != want {
		t.Fatalf("expected table %s existence=%t, got %t", table, want, exists)
	}
}

func releaseGateDSN() (string, bool, error) {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(githubActionsEnv)), githubActionsValue) {
		if strings.TrimSpace(os.Getenv("DATABASE_URL")) != "" {
			return "", false, fmt.Errorf("DATABASE_URL is not permitted for the GitHub Actions release gate")
		}
		dsn, configured, err := releaseGateDBVars(true)
		if err != nil {
			return "", false, err
		}
		if !configured {
			return "", false, fmt.Errorf("database configuration is required when the PostgreSQL release gate is authorized")
		}
		return dsn, true, nil
	}

	if value, present := os.LookupEnv(localReleaseGateEnv); present {
		if strings.TrimSpace(value) != localReleaseGateValue {
			return "", false, fmt.Errorf("%s must be %q when set", localReleaseGateEnv, localReleaseGateValue)
		}
	} else {
		return "", false, nil
	}

	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		if err := validateLocalReleaseGateDSN(dsn); err != nil {
			return "", false, err
		}
		return dsn, true, nil
	}
	dsn, configured, err := releaseGateDBVars(false)
	if err != nil {
		return "", false, err
	}
	if !configured {
		return "", false, fmt.Errorf("database configuration is required when the PostgreSQL release gate is authorized")
	}
	return dsn, true, nil
}

func validateLocalReleaseGateDSN(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Path != "/"+localReleaseGateDatabaseName {
		return fmt.Errorf("DATABASE_URL must target the dedicated local PostgreSQL release-gate database")
	}
	if !isLoopbackReleaseGateHost(parsed.Hostname()) {
		return fmt.Errorf("DATABASE_URL must target a loopback PostgreSQL host")
	}
	return nil
}

func isLoopbackReleaseGateHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func releaseGateDBVars(ci bool) (string, bool, error) {
	names := []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE"}
	setCount := 0
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, present := os.LookupEnv(name)
		if present {
			setCount++
			values[name] = strings.TrimSpace(value)
		}
	}
	if setCount == 0 {
		return "", false, nil
	}
	if setCount != len(names) {
		return "", false, fmt.Errorf("all DB_HOST, DB_PORT, DB_USER, DB_PASSWORD, DB_NAME, and DB_SSLMODE variables are required")
	}
	for _, name := range names {
		if values[name] == "" {
			return "", false, fmt.Errorf("%s must not be empty", name)
		}
	}

	if ci {
		expected := map[string]string{
			"DB_HOST":     ciDatabaseHost,
			"DB_PORT":     ciDatabasePort,
			"DB_USER":     ciDatabaseUser,
			"DB_PASSWORD": ciDatabasePassword,
			"DB_NAME":     ciDatabaseName,
			"DB_SSLMODE":  ciDatabaseSSLMode,
		}
		for name, want := range expected {
			if values[name] != want {
				return "", false, fmt.Errorf("%s must target the GitHub Actions disposable PostgreSQL service", name)
			}
		}
	} else {
		if !isLoopbackReleaseGateHost(values["DB_HOST"]) {
			return "", false, fmt.Errorf("DB_HOST must target a loopback PostgreSQL host")
		}
		if values["DB_NAME"] != localReleaseGateDatabaseName {
			return "", false, fmt.Errorf("DB_NAME must target the dedicated local PostgreSQL release-gate database")
		}
	}

	query := url.Values{}
	query.Set("sslmode", values["DB_SSLMODE"])
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(values["DB_USER"], values["DB_PASSWORD"]),
		Host:     net.JoinHostPort(values["DB_HOST"], values["DB_PORT"]),
		Path:     "/" + values["DB_NAME"],
		RawQuery: query.Encode(),
	}).String(), true, nil
}

func releaseGateMigrationDir(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "migrations"))
}
