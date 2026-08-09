# Disbursement API v2

A high-performance, concurrency-safe, and production-ready Disbursement REST API built with Go 1.21, Gin, PostgreSQL, and `sqlx`.

PostgreSQL serves as the single source of truth and transactional boundary for business domain mutations, fenced idempotency claims, refresh session rotation, and durable audit outbox events. Detailed architectural design decisions are documented in [`ARCHITECTURE.md`](ARCHITECTURE.md).

---

## Technical Stack

| Category | Technology |
|---|---|
| **Language** | Go 1.21 |
| **HTTP Framework** | Gin Web Framework (`v1.10.0`) |
| **Database** | PostgreSQL 15 / 16 |
| **Database Driver & Abstraction** | `lib/pq` (`v1.10.9`) & `sqlx` (`v1.4.0`) |
| **Migration Tooling** | `golang-migrate/migrate` (`v4.17.1`) |
| **Auth & Security** | `golang-jwt/jwt` (`v5.2.2`), `golang.org/x/crypto/bcrypt` |
| **Validation** | `go-playground/validator` (`v10.22.0`) |
| **UUID Generator** | `google/uuid` (`v1.6.0`) |
| **Observability** | Prometheus client (`prometheus/client_golang`) |
| **SQL Testing** | `DATA-DOG/go-sqlmock` (`v1.5.2`) |
| **Container** | Docker (multi-stage, `alpine:3.19`) |
| **CI** | GitHub Actions (`ubuntu-latest`, `postgres:15-alpine`) |

---

## Architecture & System Design

Detailed architectural decision records and system design choices are documented in [`ARCHITECTURE.md`](ARCHITECTURE.md). Key implementation highlights include:

### 1. Identity, Access & Session Management (`internal/service/auth` & `internal/httpapi/middleware`)
- **Stateless Bearer JWT Authentication:** Issues access JWTs signed with HMAC-SHA256 (`JWT_SECRET`) carrying `sub`, `username`, `role`, `iss`, `aud`, `iat`, and `exp` (15m TTL). Middleware explicitly verifies HMAC signing (`*jwt.SigningMethodHMAC`) and validates mandatory `iss` and `aud` claims to prevent `none` algorithm & key-confusion attacks.
- **PostgreSQL-Backed Refresh Rotation:** Cryptographic UUID v4 refresh tokens are stored solely as SHA-256 hashes (`token_hash`). Plaintext tokens are never persisted or logged. `POST /auth/refresh` executes atomic session rotation (`revoked_at = now()`, `replaced_by_id = new_session.id`) and new session insertion in a single PostgreSQL transaction.
- **Single-Winner Race Protection:** PostgreSQL row-level locks and `UPDATE ... WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $1` ensure N concurrent refresh requests produce exactly one winner (`RowsAffected == 1`). All concurrent losers or reused tokens receive `401 INVALID_REFRESH_TOKEN`.
- **IP Rate Limiting:** `/auth/login` and `/auth/refresh` are protected by an in-memory IP rate limiter (max 10 requests/minute per IP). Exceeded requests return `429 TOO_MANY_REQUESTS` with `Retry-After: 60`.
- **Idempotent Logout:** `POST /auth/logout` atomically revokes refresh sessions. Repeated logouts return `204 No Content` without error.
- **Role-Based Access Control (RBAC):** `RequireRole` middleware enforces role boundaries (`OPERATOR`, `ADMIN`, `SUPERADMIN`), returning `403 FORBIDDEN` when permissions are insufficient.

### 2. Domain Business Rules & Calculations (`internal/domain`)
- **Tiered Admin Fee Calculation:** Automatically computes `2,500` IDR for disbursement amounts `< 5,000,000` IDR and `5,000` IDR for amounts `>= 5,000,000` IDR.
- **Strict Currency & Input Bounds:** Enforces amount range between `10,000` IDR and `100,000,000,000` IDR. Validates recipient names (max 150 Unicode runes), bank account numbers (6–34 digits), bank codes (3–10 alphanumeric uppercase), and notes (max 500 Unicode runes).
- **State Transition Boundaries:** Enforces state transition rules (`PENDING` can transition to `APPROVED` or `REJECTED`; final states cannot be altered).
- **Soft Delete Rules:** Restricts soft deletion (`CanDelete`) strictly to `PENDING` disbursements that have not already been deleted (`deleted_at IS NULL`).
- **UTC Date Range & Pagination:** `NewUTCDateRange` constructs exact half-open UTC date intervals `[date_from 00:00:00 UTC, date_to + 1 day 00:00:00 UTC)`. `NewPagination` computes total pages and offsets.

### 3. Fenced Idempotency Coordinator (`internal/service/idempotency` & `internal/repository/postgres`)
- **SHA-256 Canonical Fingerprinting:** Computes a canonical SHA-256 digest over the HTTP method, endpoint, and canonicalized JSON payload.
- **Timing Attack Resistance:** Uses `crypto/subtle.ConstantTimeCompare` for fingerprint verification, preventing side-channel timing analysis.
- **Atomic Claim Acquisition & Reclaim:** Executes `INSERT INTO idempotency_keys ... ON CONFLICT DO NOTHING`. Reclaims stale active claims (lease expired) or expired keys (24h past) atomically with a new random `claim_id` UUID.
- **Fenced Row Locking:** Verifies claim ownership via `SELECT ... FOR UPDATE` inside the business transaction using the owner `claim_id`.
- **Semantic Replay:** Replays completed responses for 24 hours with `X-Idempotent-Replayed: true` header.

### 4. Disbursement Workflow & Transactional Outbox (`internal/service/disbursement` & `internal/repository/postgres`)
- **Create Disbursement Slice:** Binds server fee calculations, sets status to `PENDING`, extracts `created_by` from JWT identity, parses `Idempotency-Key` headers, and writes a durable `audit_outbox` event in a single PostgreSQL transaction (`Transactor.WithinTransaction`).
- **List & Detail Query Slice:** `GET /disbursements` provides parameterized trigram search (`ILIKE` on `recipient_name` and `account_number`), status filtering, UTC date range filtering (`[start_date, end_date)`), whitelisted sort ordering (`amount`, `recipient_name`, `status`, `created_at`), and active row isolation (`deleted_at IS NULL`). `GET /disbursements/:id` maps `decided_by` along with legacy `approved_by` aliases.
- **Single-Winner Atomic Finalization:** `PATCH /disbursements/:id/status` executes single-winner SQL updates (`UPDATE disbursements SET status = $1 ... WHERE id = $5 AND status = 'PENDING' AND deleted_at IS NULL`). Concurrent losing requests receive `409 DISBURSEMENT_ALREADY_FINALIZED`.
- **Idempotent Soft Delete:** `DELETE /disbursements/:id` performs soft deletion (`deleted_at = now()`) for `SUPERADMIN` role on `PENDING` resources, returning `204 No Content` idempotently on repeat calls without duplicate outbox writes.
- **Transactional Outbox Consistency:** All mutations write immutable audit events into `audit_outbox` within the caller's database transaction. Any outbox insert failure triggers a complete rollback of the business mutation (*zero audit data loss*).

### 5. Integration Release Gates & Contract Verification (`internal/integration` & `internal/httpapi`)
- **PostgreSQL Release Gate Harness (`internal/integration/postgres_release_gate_test.go`):** Validates full schema migration roll-up/tear-down (`UpDownUp`), single-winner idempotency claim acquisition under parallel goroutines, single disbursement creation & outbox event insertion under N-way concurrent requests, atomic finalization status locking, and atomic refresh token rotation on real PostgreSQL.
- **Strict Environment Safety:** Release-gate integration tests enforce loopback host targets (`localhost`/`127.0.0.1`), dedicated database names (`disbursement_api_release_gate_test`), and fresh public schema safety validation (`assertFreshReleaseGateDatabase`) to prevent accidental execution against non-test databases.
- **HTTP Release Contract Suite (`internal/httpapi/release_contract_test.go`):** Enforces standardized JSON error response formatting, `X-Request-ID` propagation, 204 No Content for bodyless responses (`logout`, `delete`), `X-Idempotent-Replayed: true` headers, and `Retry-After` headers for in-progress claims.

---

## Database Schema

The database consists of the following core PostgreSQL tables:

```sql
-- 1. Users Table
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(50) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(20) NOT NULL CHECK (role IN ('OPERATOR', 'ADMIN', 'SUPERADMIN')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. Refresh Sessions Table
CREATE TABLE refresh_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    replaced_by_id UUID REFERENCES refresh_sessions(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3. Disbursements Table
CREATE TABLE disbursements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_name VARCHAR(150) NOT NULL,
    account_number VARCHAR(34) NOT NULL,
    bank_code VARCHAR(10) NOT NULL,
    amount BIGINT NOT NULL CHECK (amount >= 10000 AND amount <= 100000000000),
    admin_fee BIGINT NOT NULL CHECK (admin_fee IN (2500, 5000)),
    status VARCHAR(20) NOT NULL CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
    note TEXT,
    created_by UUID NOT NULL REFERENCES users(id),
    decided_by UUID REFERENCES users(id),
    decision_note TEXT,
    decided_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes for performance & search
CREATE INDEX idx_disbursements_status ON disbursements(status) WHERE deleted_at IS NULL;
CREATE INDEX idx_disbursements_created_at ON disbursements(created_at);
CREATE INDEX idx_disbursements_trigram_search ON disbursements USING gin (recipient_name gin_trgm_ops, account_number gin_trgm_ops);

-- 4. Idempotency Keys Table
CREATE TABLE idempotency_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id),
    endpoint VARCHAR(255) NOT NULL,
    idempotency_key UUID NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    claim_id UUID NOT NULL,
    status VARCHAR(20) NOT NULL CHECK (status IN ('IN_PROGRESS', 'COMPLETED')),
    response_code INT,
    response_body JSONB,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, endpoint, idempotency_key)
);

-- 5. Audit Outbox Table
CREATE TABLE audit_outbox (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type VARCHAR(50) NOT NULL,
    entity_id UUID NOT NULL,
    action VARCHAR(50) NOT NULL,
    actor_id UUID NOT NULL REFERENCES users(id),
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

---

## API Endpoints Reference

### Public Authentication Endpoints (`/auth`)

| Method | Path | Description | Access |
|---|---|---|---|
| `POST` | `/auth/login` | Login with username and password (rate limited: 10 req/min) | Public |
| `POST` | `/auth/refresh` | Rotate refresh token for a new access token (rate limited: 10 req/min) | Public |
| `POST` | `/auth/logout` | Revoke refresh token | Public |

### Protected Disbursement Endpoints (`/api/v1/disbursements`)

| Method | Path | Description | RBAC Role Access |
|---|---|---|---|
| `POST` | `/api/v1/disbursements` | Create a new disbursement | `OPERATOR`, `ADMIN`, `SUPERADMIN` |
| `GET` | `/api/v1/disbursements` | List disbursements (with search, filter, sort) | `OPERATOR`, `ADMIN`, `SUPERADMIN` |
| `GET` | `/api/v1/disbursements/:id` | Get disbursement detail by ID | `OPERATOR`, `ADMIN`, `SUPERADMIN` |
| `PATCH` | `/api/v1/disbursements/:id/status` | Finalize disbursement (`APPROVED`/`REJECTED`) | `ADMIN`, `SUPERADMIN` |
| `DELETE` | `/api/v1/disbursements/:id` | Soft delete pending disbursement | `SUPERADMIN` |

### Internal Observability Endpoint

| Method | Path | Description | Access |
|---|---|---|---|
| `GET` | `/metrics` | Prometheus operational metrics | `X-Metrics-Token` header required |

---

## Getting Started

### Prerequisites
- Go `1.21` or newer
- PostgreSQL `16` or newer

---

### 1. Environment Configuration

Set mandatory environment variables:

```bash
export DATABASE_URL="postgres://<user>:<password>@localhost:5432/disbursement_db?sslmode=disable"
export JWT_SECRET="super-secret-key-minimum-32-characters"
export HTTP_ADDRESS=":8080"
```

#### Supported Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | - | PostgreSQL connection DSN URL |
| `JWT_SECRET` | **Yes** | - | Secret key for JWT signing and verification |
| `JWT_ISSUER` | **Yes** | - | Expected JWT `iss` claim value |
| `JWT_AUDIENCE` | **Yes** | - | Expected JWT `aud` claim value |
| `METRICS_TOKEN` | **Yes** | - | Bearer token for `/metrics` endpoint (`X-Metrics-Token` header) |
| `HTTP_ADDRESS` | No | `:8080` | Bind address for HTTP server |
| `HTTP_READ_TIMEOUT` | No | `10s` | HTTP server read timeout |
| `HTTP_WRITE_TIMEOUT` | No | `15s` | HTTP server write timeout |
| `HTTP_IDLE_TIMEOUT` | No | `60s` | HTTP server idle connection timeout |
| `SHUTDOWN_TIMEOUT` | No | `10s` | Maximum grace period for server shutdown |
| `MAX_REQUEST_BODY_BYTES` | No | `1048576` | Maximum request body limit (default 1 MiB) |
| `TRUSTED_PROXIES` | No | - | Comma-separated list of trusted reverse proxy CIDRs |
| `DB_MAX_OPEN_CONNS` | No | `20` | Maximum open database pool connections |
| `DB_MAX_IDLE_CONNS` | No | `10` | Maximum idle database pool connections |
| `DB_CONN_MAX_LIFETIME` | No | `30m` | Connection maximum lifetime |
| `ACCESS_TOKEN_TTL` | No | `15m` | JWT Access Token duration |
| `REFRESH_TOKEN_TTL` | No | `168h` | Refresh Token duration (7 days) |
| `IDEMPOTENCY_LEASE_TTL` | No | `30s` | Active idempotency claim lease duration |
| `IDEMPOTENCY_REPLAY_TTL` | No | `24h` | Idempotency response replay retention |
| `AUDIT_OUTBOX_BATCH_SIZE` | No | `100` | Number of outbox events claimed per relay cycle |
| `AUDIT_RELAY_INTERVAL` | No | `5s` | Audit relay worker polling interval |
| `AUDIT_RETENTION_DAYS` | No | `30` | Days before delivered outbox staging records are pruned |
| `POSTGRES_RELEASE_GATE` | No | `0` | Set to `1` to authorize local PostgreSQL release gate integration tests |

---

### 2. Database Migration & Seeding

Run all schema migrations and seed local test accounts (`testoperator`, `testadmin`, `testsuperadmin`):

```bash
# Apply schema migrations and idempotent local seed
go run ./cmd/migrate -action up -seed

# Rollback all migrations
go run ./cmd/migrate -action down
```

---

### 3. Running the Server

Start the API HTTP server:

```bash
go run ./cmd/api
```

---

### 4. Running the Test Suite

Execute the deterministic unit test suite and PostgreSQL integration release gate:

```bash
# 1. Run all unit & release contract tests with race detector
go test -v -race ./...

# 2. Run PostgreSQL Release Gate Integration Tests (requires POSTGRES_RELEASE_GATE=1)
POSTGRES_RELEASE_GATE=1 \
DATABASE_URL="postgres://<user>:<password>@localhost:5432/disbursement_api_release_gate_test?sslmode=disable" \
go test -v -run TestPostgreSQLReleaseGate ./internal/integration/...

# 3. Run with coverage profile
go test -v -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

### 5. Docker Build & Run

```bash
# Build production image
docker build -t disbursement-api:latest .

# Run container
docker run -p 8080:8080 \
  -e DATABASE_URL="postgres://<user>:<password>@host.docker.internal:5432/disbursement_db?sslmode=disable" \
  -e JWT_SECRET="your-secret-key-minimum-32-characters" \
  -e JWT_ISSUER="disbursement-api" \
  -e JWT_AUDIENCE="disbursement-api-users" \
  -e METRICS_TOKEN="your-metrics-token" \
  disbursement-api:latest
```

---

## Quick Testing cURL Examples

### 1. User Login (Admin)
```bash
curl -i -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "testadmin",
    "password": "Password123!"
  }'
```

### 2. Create Disbursement (with Idempotency Key)
```bash
curl -i -X POST http://localhost:8080/api/v1/disbursements \
  -H "Authorization: Bearer <ACCESS_TOKEN>" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: 9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d" \
  -d '{
    "recipient_name": "Budi Santoso",
    "account_number": "1234567890",
    "bank_code": "BCA",
    "amount": 2500000,
    "note": "Pembayaran Vendor Q3"
  }'
```

### 3. List Disbursements (Search & UTC Date Filter)
```bash
curl -i -X GET "http://localhost:8080/api/v1/disbursements?page=1&limit=10&status=PENDING&search=Budi&sort_by=amount&sort_order=asc&start_date=2026-08-01T00:00:00Z&end_date=2026-08-31T23:59:59Z" \
  -H "Authorization: Bearer <ACCESS_TOKEN>"
```

### 4. Approve Disbursement Status
```bash
curl -i -X PATCH http://localhost:8080/api/v1/disbursements/<DISBURSEMENT_ID>/status \
  -H "Authorization: Bearer <ADMIN_ACCESS_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "status": "APPROVED",
    "note": "Disetujui sesuai invoice"
  }'
```

### 5. Soft Delete Pending Disbursement (Superadmin)
```bash
curl -i -X DELETE http://localhost:8080/api/v1/disbursements/<DISBURSEMENT_ID> \
  -H "Authorization: Bearer <SUPERADMIN_ACCESS_TOKEN>"
```

---

## API Envelopes & Contract Specification

### 1. Success Response Envelope (`HTTP 200 / 201`)

```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "recipient_name": "Budi Santoso",
    "account_number": "1234567890",
    "bank_code": "BCA",
    "amount": 2500000,
    "admin_fee": 2500,
    "status": "PENDING",
    "note": "Pembayaran Vendor Q3",
    "created_by": "110e8400-e29b-41d4-a716-446655440000",
    "created_at": "2026-08-08T10:00:00Z",
    "updated_at": "2026-08-08T10:00:00Z"
  }
}
```

### 2. No Content Response (`HTTP 204`)
- Body: **Empty (0 bytes)**
- Headers: `X-Request-ID` is preserved.

### 3. Error Response Envelope (`HTTP 400 / 401 / 403 / 404 / 409 / 500`)

```json
{
  "success": false,
  "error": {
    "code": "DISBURSEMENT_ALREADY_FINALIZED",
    "message": "Disbursement sudah berada dalam status final dan tidak dapat diubah"
  },
  "request_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### 4. Standard Domain Error Codes

| Error Code | HTTP Status | Trigger Condition |
|---|---|---|
| `VALIDATION_ERROR` | `400` | Malformed JSON, unknown fields, or struct validation failed |
| `INVALID_IDEMPOTENCY_KEY` | `400` | Missing or non-UUID v4 `Idempotency-Key` header |
| `INVALID_CREDENTIALS` | `401` | Invalid username or password credential |
| `UNAUTHORIZED` | `401` | Missing, invalid, or expired authentication token |
| `INVALID_REFRESH_TOKEN` | `401` | Expired, revoked, or reused refresh token |
| `FORBIDDEN` | `403` | Role lacks permission for target endpoint |
| `DISBURSEMENT_NOT_FOUND` | `404` | Disbursement ID not found or soft-deleted |
| `DISBURSEMENT_ALREADY_FINALIZED` | `409` | Resource is already `APPROVED` or `REJECTED` |
| `DISBURSEMENT_NOT_DELETABLE` | `409` | Soft delete requested on non-`PENDING` disbursement |
| `CONCURRENT_MODIFICATION` | `409` | Lost finalization race against concurrent request |
| `IDEMPOTENCY_KEY_REUSED` | `409` | Idempotency key reused with different request payload |
| `IDEMPOTENCY_REQUEST_IN_PROGRESS` | `409` | Idempotency key currently locked by active request |
| `TOO_MANY_REQUESTS` | `429` | IP rate limit exceeded on `/auth/login` or `/auth/refresh` |
| `INTERNAL_ERROR` | `500` | Unhandled internal error or panic |

---

## Observability & Structured Logging

Every request generates a single structured JSON log emitted to `stdout`. Sensitive fields (`password`, `authorization`, `account_number`, `token`, `email`, `phone`, `tax_id`, `bank_account`) are automatically masked into `[REDACTED]`.

Prometheus metrics are available at `GET /metrics` (requires `X-Metrics-Token` header). Tracked metrics include outbox backlog count, idempotency outcomes, finalization conflict counters, HTTP request latencies, and DB pool connection stats.

Example Access Log:
```json
{
  "time": "2026-08-08T10:00:00Z",
  "level": "INFO",
  "msg": "request completed",
  "request_id": "550e8400-e29b-41d4-a716-446655440000",
  "method": "POST",
  "path": "/api/v1/disbursements",
  "status_code": 201,
  "latency_ms": 42
}
```
