# Disbursement API v2

A high-performance, concurrency-safe, and production-ready Disbursement REST API built with Go 1.21, Gin, PostgreSQL, and `sqlx`.

PostgreSQL serves as the single source of truth and transactional boundary for business domain mutations, fenced idempotency, and durable audit outbox events.

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

## Key Architecture & Design Highlights

Detailed architectural decisions are documented in [`ARCHITECTURE.md`](ARCHITECTURE.md). Key design highlights include:

### 1. Fenced Idempotency (`POST /disbursements`)
- Uses PostgreSQL `idempotency_keys` table scoped by `(user_id, endpoint, key)`.
- Enforces atomic claims via random `claim_id` UUIDs, lease expiration, and 24-hour semantic JSON replay with `X-Idempotent-Replayed: true`.
- Prevents stale owner commits and duplicate creation during concurrent retries using database row locks (`SELECT ... FOR UPDATE`) and owner `claim_id` verification inside the business transaction.

### 2. Concurrency-Safe Finalization & Status Transitions
- Approval and rejection use single-statement SQL conditional updates:
  ```sql
  UPDATE disbursements
  SET status = $2, decided_by = $3, decision_note = $4, decided_at = now(), updated_at = now()
  WHERE id = $1 AND status = 'PENDING' AND deleted_at IS NULL
  RETURNING *;
  ```
- Evaluates status preconditions atomically at the database engine level under `READ COMMITTED` isolation, eliminating lost updates without the overhead or complexity of distributed locks or application-side read-then-write locks.
- Returns `409 CONCURRENT_MODIFICATION` for concurrent modification races and `404 DISBURSEMENT_NOT_FOUND` if the resource does not exist or is soft-deleted.

### 3. Transactional Audit Outbox & Non-Repudiation
- All mutations (`create`, `finalize`, `soft-delete`) write immutable audit events into `audit_outbox` within the exact same database transaction as the domain state change.
- A Pl/pgSQL trigger `prevent_audit_outbox_event_id_change()` enforces strict immutability on `audit_outbox.event_id`.
- `audit_logs.source_event_id` enforces unique projection constraint to guarantee idempotent event delivery.

---

## Directory Structure

```
.
├── cmd/
│   ├── api/             # Main HTTP API application entrypoint
│   └── migrate/         # Database migration and seed CLI tool
├── internal/
│   ├── app/             # Application wiring, server lifecycle, and graceful shutdown
│   ├── config/          # Environment configuration parser with fail-fast validation
│   ├── database/        # PostgreSQL connection pool setup and context ping
│   ├── dependencies/    # Dependency tracking package
│   ├── domain/          # Core domain models and standardized error types
│   ├── httpapi/
│   │   ├── binding/     # Strict JSON body decoder (disallows unknown fields)
│   │   ├── dto/         # Request DTOs and query parameters
│   │   ├── middleware/  # RequestID, Recovery, AccessLog, and BodyLimit middleware
│   │   ├── response/    # Standardized JSON success and error response envelopes
│   │   ├── validation/  # Custom validator with Unicode rune count checks
│   │   └── router.go    # Gin engine setup and NoRoute fallback
│   ├── migration/       # Wrapper for golang-migrate and SQL seed execution
│   └── observability/
│       └── redaction/   # Automatic log redaction for sensitive fields
├── migrations/          # Raw SQL schema migration files and idempotent seed
├── ARCHITECTURE.md      # Standalone architectural decision document
└── README.md            # Project documentation handbook
```

---

## Getting Started

### Prerequisites
- Go `1.21` or newer
- PostgreSQL `16` or newer running locally or via Docker

---

### 1. Environment Configuration

Copy or set mandatory environment variables:

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

Run all `UP` migrations and seed local test accounts (`local_test_operator`, `local_test_admin`, `local_test_superadmin`):

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

Execute the deterministic unit test suite:

```bash
# Run unit tests across all packages
CGO_ENABLED=0 GOROOT=$(go env GOROOT) go test -v ./...
```

---

## API Envelopes & Contract Specification

### 1. Success Response (`HTTP 200 / 201`)

```json
{
  "success": true,
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "recipient_name": "Budi Santoso",
    "amount": 1250000,
    "admin_fee": 2500,
    "status": "PENDING"
  },
  "meta": {
    "page": 1,
    "limit": 20,
    "total": 1,
    "total_pages": 1
  }
}
```

### 2. No Content Response (`HTTP 204`)
- Body: **Empty (0 bytes)**
- Headers: `X-Request-ID` is preserved.

### 3. Error Response (`HTTP 400 / 401 / 403 / 404 / 409 / 500`)

```json
{
  "success": false,
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "Input tidak valid",
    "details": [
      {
        "field": "recipient_name",
        "message": "panjang maksimum 150"
      }
    ]
  },
  "request_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

### 4. Standard Domain Error Codes

| Error Code | HTTP Status | Meaning |
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
  "path": "/disbursements",
  "status_code": 201,
  "latency_ms": 14
}
```
