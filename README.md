# Disbursement API v2

A high-performance, concurrency-safe, and production-ready Disbursement REST API built with Go 1.21, Gin, PostgreSQL, and `sqlx`.

PostgreSQL serves as the single source of truth and transactional boundary for business domain mutations, fenced idempotency claims, and durable audit outbox events.

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

### 1. Domain Business Rules & Calculations (`internal/domain`)
- **Tiered Admin Fee Calculation:** Automatically computes `2,500` IDR for disbursement amounts `< 5,000,000` IDR and `5,000` IDR for amounts `>= 5,000,000` IDR.
- **Strict Currency & Input Bounds:** Enforces amount range between `10,000` IDR and `100,000,000,000` IDR. Validates recipient names (max 150 Unicode runes), bank account numbers (6–34 digits), bank codes (3–10 alphanumeric uppercase), and notes (max 500 Unicode runes).
- **State Transition Boundaries:** Enforces state transition rules (`PENDING` can transition to `APPROVED` or `REJECTED`; final states cannot be altered).
- **Soft Delete Rules:** Restricts soft deletion (`CanDelete`) strictly to `PENDING` disbursements that have not already been deleted (`deleted_at IS NULL`).
- **UTC Date Range & Pagination:** `NewUTCDateRange` constructs exact half-open UTC date intervals `[date_from 00:00:00 UTC, date_to + 1 day 00:00:00 UTC)`. `NewPagination` computes total pages and offsets.

### 2. Fenced Idempotency Coordinator (`internal/service/idempotency` & `internal/repository/postgres`)
- **SHA-256 Canonical Fingerprinting:** Computes a canonical SHA-256 digest over the HTTP method, endpoint, and canonicalized JSON payload.
- **Timing Attack Resistance:** Uses `crypto/subtle.ConstantTimeCompare` for fingerprint verification, preventing side-channel timing analysis.
- **Atomic Claim Acquisition & Reclaim:** Executes `INSERT INTO idempotency_keys ... ON CONFLICT DO NOTHING`. Reclaims stale active claims (lease expired) or expired keys (24h past) atomically with a new random `claim_id` UUID.
- **Fenced Row Locking:** Verifies claim ownership via `SELECT ... FOR UPDATE` inside the business transaction using the owner `claim_id`.
- **Semantic Replay:** Replays completed responses for 24 hours with `X-Idempotent-Replayed: true` header.

### 3. Transactional Audit Outbox & Redaction (`internal/domain` & `internal/repository/postgres`)
- **Transactional Consistency:** All mutations write immutable audit events into `audit_outbox` within the caller's database transaction (`Transactor.WithinTransaction`).
- **Automatic PII Redaction:** `AuditSnapshot` inspects payloads and automatically masks sensitive fields (`password`, `authorization`, `account_number`, `token`) into `[REDACTED]`.
- **Database Immutability:** Pl/pgSQL trigger `prevent_audit_outbox_event_id_change()` prevents any modification to `audit_outbox.event_id`.

### 4. Central Sensitivity Package (`internal/sensitivity`)
- Exports `IsSensitiveKey(key string) bool` to unify key redaction across domain audit snapshots and observability log attributes without cross-layer package coupling.

### 5. Repository Abstraction & Error Classification (`internal/repository`)
- **Transaction Scoping:** `Transactor.WithinTransaction` provides safe transaction scoping and automatic rollback on error.
- **Error Classification:** `Classify(err)` translates raw database errors into unified categories (`ErrorNotFound`, `ErrorConflict`, `ErrorConstraint`, `ErrorDependency`).

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
│   ├── domain/          # Core domain models, fee rules, validation, and audit events
│   ├── httpapi/
│   │   ├── binding/     # Strict JSON body decoder (disallows unknown fields)
│   │   ├── dto/         # Request DTOs and query parameters
│   │   ├── middleware/  # RequestID, Recovery, AccessLog, and BodyLimit middleware
│   │   ├── response/    # Standardized JSON success and error response envelopes
│   │   ├── validation/  # Custom validator with Unicode rune count checks
│   │   └── router.go    # Gin engine setup and NoRoute fallback
│   ├── migration/       # Wrapper for golang-migrate and SQL seed execution
│   ├── observability/
│   │   └── redaction/   # Log attribute redaction for sensitive fields
│   ├── repository/
│   │   ├── postgres/    # PostgreSQL transaction, idempotency, and outbox stores
│   │   ├── contracts.go # Service-facing repository interfaces
│   │   └── errors.go    # Repository error classification
│   ├── sensitivity/     # Centralized sensitive key detection utility
│   └── service/
│       └── idempotency/ # Fenced idempotency coordinator and SHA-256 fingerprinting
├── migrations/          # Raw SQL schema migration files and idempotent seed
├── ARCHITECTURE.md      # Architectural design and system decisions document
└── README.md            # Main developer handbook
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

Execute the deterministic unit test suite with statement coverage:

```bash
# Run unit tests across all packages
CGO_ENABLED=0 GOROOT=$(go env GOROOT) go test -v -cover ./...
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

### 3. Error Response Envelope (`HTTP 400 / 401 / 403 / 404 / 409 / 500`)

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
  "path": "/disbursements",
  "status_code": 201,
  "latency_ms": 14
}
```
