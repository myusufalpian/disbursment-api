# Disbursement API v2

A high-performance, concurrency-safe, and production-ready Disbursement REST API built with Go 1.21, Gin, PostgreSQL, and `sqlx`.

PostgreSQL serves as the single source of truth and transactional boundary for business domain mutations, fenced idempotency claims, refresh session rotation, and durable audit outbox events.

---

## Technical Stack

| Category | Technology |
|---|---|
| **Language** | Go 1.21 |
| **HTTP Framework** | Gin Web Framework (`v1.10.0`) |
| **Database** | PostgreSQL 16 |
| **Database Driver & Abstraction** | `lib/pq` (`v1.10.9`) & `sqlx` (`v1.4.0`) |
| **Migration Tooling** | `golang-migrate/migrate` (`v4.17.1`) |
| **Auth & Security** | `golang-jwt/jwt` (`v5.2.1`), `golang.org/x/crypto/bcrypt` |
| **Validation** | `go-playground/validator` (`v10.22.0`) |
| **UUID Generator** | `google/uuid` (`v1.6.0`) |

---

## Architecture & System Design

Detailed architectural decision records and system design choices are documented in [`ARCHITECTURE.md`](ARCHITECTURE.md). Key implementation highlights include:

### 1. Identity, Access & Session Management (`internal/service/auth` & `internal/httpapi/middleware`)
- **Stateless Bearer JWT Authentication:** Issues access JWTs signed with HMAC-SHA256 (`JWT_SECRET`) carrying `sub`, `username`, `role`, `iat`, and `exp` (15m TTL). Middleware explicitly verifies HMAC signing (`*jwt.SigningMethodHMAC`) to prevent `none` algorithm & key-confusion attacks.
- **PostgreSQL-Backed Refresh Rotation:** Cryptographic UUID v4 refresh tokens are stored solely as SHA-256 hashes (`token_hash`). Plaintext tokens are never persisted or logged. `POST /auth/refresh` executes atomic session rotation (`revoked_at = now()`, `replaced_by_id = new_session.id`) and new session insertion in a single PostgreSQL transaction.
- **Single-Winner Race Protection:** PostgreSQL row-level locks and `UPDATE ... WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $1` ensure N concurrent refresh requests produce exactly one winner (`RowsAffected == 1`). All concurrent losers or reused tokens receive `401 INVALID_REFRESH_TOKEN`.
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

### 4. Transactional Audit Outbox & Redaction (`internal/domain` & `internal/repository/postgres`)
- **Transactional Consistency:** All mutations write immutable audit events into `audit_outbox` within the caller's database transaction (`Transactor.WithinTransaction`).
- **Automatic Sensitive Redaction:** `AuditSnapshot` inspects payloads and automatically masks sensitive fields (`password`, `authorization`, `account_number`, `token`) into `[REDACTED]`.
- **Central Sensitivity Package:** `sensitivity.IsSensitiveKey(key string)` unifies key redaction across domain audit snapshots and observability log attributes without cross-layer package coupling.

---

## Getting Started

### Prerequisites
- Go `1.21` or newer
- PostgreSQL `16` or newer

---

### 1. Environment Configuration

Set mandatory environment variables:

```bash
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/disbursement_db?sslmode=disable"
export JWT_SECRET="super-secret-key-minimum-32-characters"
export HTTP_ADDRESS=":8080"
```

#### Supported Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | **Yes** | - | PostgreSQL connection DSN URL |
| `JWT_SECRET` | **Yes** | - | Secret key for JWT signing and verification |
| `HTTP_ADDRESS` | No | `:8080` | Bind address for HTTP server |
| `HTTP_READ_TIMEOUT` | No | `10s` | HTTP server read timeout |
| `HTTP_WRITE_TIMEOUT` | No | `15s` | HTTP server write timeout |
| `HTTP_IDLE_TIMEOUT` | No | `60s` | HTTP server idle connection timeout |
| `SHUTDOWN_TIMEOUT` | No | `10s` | Maximum grace period for server shutdown |
| `MAX_REQUEST_BODY_BYTES` | No | `1048576` | Maximum request body limit (default 1 MiB) |
| `DB_MAX_OPEN_CONNS` | No | `20` | Maximum open database pool connections |
| `DB_MAX_IDLE_CONNS` | No | `10` | Maximum idle database pool connections |
| `DB_CONN_MAX_LIFETIME` | No | `30m` | Connection maximum lifetime |
| `ACCESS_TOKEN_TTL` | No | `15m` | JWT Access Token duration |
| `REFRESH_TOKEN_TTL` | No | `168h` | Refresh Token duration (7 days) |
| `IDEMPOTENCY_LEASE_TTL` | No | `30s` | Active idempotency claim lease duration |
| `IDEMPOTENCY_REPLAY_TTL` | No | `24h` | Idempotency response replay retention |

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

Execute the deterministic unit test suite with statement coverage:

```bash
# Run unit tests across all packages
CGO_ENABLED=0 GOROOT=$(go env GOROOT) GOTOOLCHAIN=local go test -coverprofile=coverage.out ./...
```

---

## Quick Testing cURL Examples

### 1. User Login
```bash
curl -i -X POST http://localhost:8080/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "testadmin",
    "password": "Password123!"
  }'
```

### 2. Refresh Token Rotation
```bash
curl -i -X POST http://localhost:8080/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "<INSERT_REFRESH_TOKEN>"
  }'
```

### 3. Accessing Authenticated Route
```bash
curl -i -X GET http://localhost:8080/api/v1/auth/me \
  -H "Authorization: Bearer <INSERT_ACCESS_TOKEN>"
```

### 4. User Logout
```bash
curl -i -X POST http://localhost:8080/auth/logout \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "<INSERT_REFRESH_TOKEN>"
  }'
```

---

## API Envelopes & Contract Specification

### 1. Success Response Envelope (`HTTP 200 / 201`)

```json
{
  "success": true,
  "data": {
    "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6...",
    "refresh_token": "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
    "token_type": "Bearer",
    "expires_in": 900,
    "refresh_expires_in": 604800
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
    "code": "INVALID_CREDENTIALS",
    "message": "Username atau password salah"
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
| `INTERNAL_ERROR` | `500` | Unhandled internal error or panic |

---

## Observability & Structured Logging

Every request generates a single structured JSON log emitted to `stdout`. Sensitive fields (`password`, `authorization`, `account_number`, `token`) are automatically masked into `[REDACTED]`.

Example Access Log:
```json
{
  "time": "2026-08-08T10:00:00Z",
  "level": "INFO",
  "msg": "request completed",
  "request_id": "550e8400-e29b-41d4-a716-446655440000",
  "method": "POST",
  "path": "/auth/login",
  "status_code": 200,
  "latency_ms": 42
}
```
