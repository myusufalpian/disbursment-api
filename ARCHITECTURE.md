# Architecture — Disbursement API v2

This document summarizes the architectural decisions and system design for Disbursement API v2. The target implementation stack consists of Go 1.21, Gin, PostgreSQL, sqlx, Docker, and GitHub Actions CI. PostgreSQL serves as the sole transaction boundary for business state, idempotency fencing, durable event audit outbox logging, and release-gate integration verification.

## 1.1 Idempotency for `POST /disbursements`

For `POST /disbursements`, an `idempotency_keys` table in PostgreSQL serves as a durable state that is transactionally committed alongside the disbursement resource. Idempotency keys are scoped by `(user_id, endpoint, key)` to isolate keys across different users. Before claiming a key, the API validates the UUID v4 header format and full request body payload; validation failures do not create an idempotency record. A request fingerprint is computed from the HTTP method, route, and canonical JSON payload. The record stores the fingerprint, state (`IN_PROGRESS` or `COMPLETED`), a random `claim_id` UUID, lease expiration timestamp, 24-hour replay window, HTTP response status, and JSON response body.

The initial request executes an atomic claim via `INSERT INTO idempotency_keys ... ON CONFLICT DO NOTHING`. Requests with a differing payload fingerprint receive `409 IDEMPOTENCY_KEY_REUSED`; active concurrent claims receive `409` with a `Retry-After` header; unexpired `COMPLETED` records replay the semantically equivalent status and JSON body along with an `X-Idempotent-Replayed: true` header. Stale active claims or expired records can be atomically reclaimed with a new `claim_id`. However, a former claim owner is never trusted simply because it once acquired the key: the business transaction locks the idempotency row using `SELECT ... FOR UPDATE` and verifies ownership of the `claim_id` before executing the resource insertion.

Within the same database transaction, the service creates the disbursement record, marks the idempotency claim completed using the `claim_id` owner predicate, and writes a durable audit outbox event. If any step fails, the entire transaction rolls back. This approach prevents duplicate resource creation even if the initial HTTP response is lost or if a delayed request resumes after lease reclamation. The trade-off is a more complex state machine and PostgreSQL concurrency testing compared to in-memory caches or Redis, but neither can commit atomically with business data without additional fencing mechanisms.

## 1.2 Concurrency and Locking for Approval

For approval or rejection, a SQL conditional update in PostgreSQL is used rather than an application-side read-then-write approach. A single SQL statement atomically evaluates business preconditions: the target resource must exist with the correct ID, be in `PENDING` status, and must not be soft-deleted. For example: `UPDATE disbursements ... WHERE id = $1 AND status = 'PENDING' AND deleted_at IS NULL RETURNING *`. A single returned row signifies that the request successfully won the finalization race; `decided_by`, status, decision note, and `updated_at` are persisted together. The returned row serves as the before/after snapshot for the audit outbox event in the same transaction.

If zero rows are returned by the update statement, the service performs a controlled diagnostic read. A non-existent or soft-deleted resource yields `404 DISBURSEMENT_NOT_FOUND`; a resource already in `APPROVED` or `REJECTED` state yields `409 DISBURSEMENT_ALREADY_FINALIZED`; a request losing the race due to simultaneous execution yields `409 CONCURRENT_MODIFICATION`. Under PostgreSQL `READ COMMITTED` isolation level, the losing update waits briefly for the row lock held by the first update, then re-evaluates the `status = 'PENDING'` predicate after commit. Because the status has transitioned to final, the second update affects zero rows. Lost updates are eliminated without requiring distributed locks.

The trade-off is that losing requests may wait briefly for a row lock and require an additional diagnostic read to yield an informative error response. This remains significantly simpler and safer than using `SELECT ... FOR UPDATE` as the primary mechanism—which holds locks longer and moves critical precondition checks into application code. Version-based optimistic locking was not selected because clients do not supply version tokens; Redis/distributed locks introduce additional failure modes without offering stronger authority than the database holding the transaction state.

## 1.3 Identity, Access, and Session Management

For authentication and access control, the system uses stateless Bearer JWTs (HMAC-SHA256) for access authorization and PostgreSQL-backed refresh sessions for token rotation. Access JWTs carry claims (`sub`, `username`, `role`, `iat`, `exp`) and are signed using a 256-bit secret configured via environment variables. Middleware explicitly verifies the HMAC signing algorithm (`*jwt.SigningMethodHMAC`) to prevent algorithm switching attacks (`none` or RSA/HMAC key confusion). Validated user identities are injected into request context using an unexported context key (`userContextKey{}`) to prevent cross-package collisions.

Refresh tokens are generated as cryptographic UUID v4 strings, but plain-text refresh tokens are never persisted or logged. Instead, only SHA-256 hashes (`token_hash`) are stored in `refresh_sessions`. Token refresh (`POST /auth/refresh`) executes an atomic rotation transaction: the old token is marked revoked (`revoked_at = now()`, `replaced_by_id = new_session.id`) and a new session is inserted within the same database transaction. The SQL `UPDATE ... WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > $1` predicate ensures single-winner rotation under PostgreSQL row locks. If concurrent refresh requests attempt to reuse the same old token, exactly one winner succeeds (`RowsAffected == 1`); all concurrent or subsequent replay attempts fail with `401 INVALID_REFRESH_TOKEN` without session duplication or state corruption.

Role-Based Access Control (RBAC) is enforced by `RequireRole` middleware, evaluating claims against defined role boundaries (`OPERATOR`, `ADMIN`, `SUPERADMIN`). Requests lacking authentication yield `401 UNAUTHORIZED`; requests with valid authentication but insufficient roles yield `403 FORBIDDEN`. All sensitive fields (`password`, `authorization`, `account_number`, `token`, `email`, `phone`, `tax_id`, `bank_account`) are automatically masked in structured logs via `sensitivity.IsSensitiveKey` redaction.

The trade-off is storing refresh session state in PostgreSQL rather than an in-memory cache like Redis. However, using PostgreSQL row-level locks for rotation avoids introducing external state infrastructure while providing single-winner transaction guarantees.

## 1.4 Disbursement Mutations & Transactional Outbox

The system integrates resource creation, list/detail query filtering, status finalization, soft deletion, and transactional audit outbox logging:

- **Disbursement Creation & Server Fee Calculation:** `POST /disbursements` enforces server-side fee calculations (`2,500` IDR for amounts < 5,000,000 IDR; `5,000` IDR for amounts ≥ 5,000,000 IDR), initializes status to `PENDING`, binds `created_by` to the authenticated JWT identity, validates `Idempotency-Key` headers, and records a durable `audit_outbox` event in the primary transaction.
- **Search, Filtering, & Read Projection:** `GET /disbursements` provides parameterized trigram search (`ILIKE` matching on `recipient_name` and `account_number`), status filtering, UTC half-open date range predicates (`[start, end)`), whitelisted sort ordering (`amount`, `recipient_name`, `status`, `created_at`), and active record isolation (`deleted_at IS NULL`). `GET /disbursements/:id` projects the `decided_by` actor along with legacy `approved_by` read aliases.
- **Status Finalization & Idempotent Soft Deletion:** `PATCH /disbursements/:id/status` executes single-winner atomic status updates for `ADMIN` and `SUPERADMIN` roles. `DELETE /disbursements/:id` executes soft deletion (`deleted_at = now()`) for `SUPERADMIN` roles on `PENDING` resources, returning `204 No Content` idempotently on repeated calls without generating duplicate outbox events.
- **Transactional Outbox Durability & Trade-off:** All business mutations (`Create`, `UpdateStatus`, `SoftDelete`) and their associated `audit_outbox` entries commit atomically within a single PostgreSQL transaction. If outbox insertion fails, the entire business mutation rolls back. The trade-off is slightly higher transaction write latency per mutation in exchange for guaranteed audit event durability (*zero audit data loss*).

## 1.5 Durable Audit Relay, Observability, Integration Release Gates, and Containerization

The audit relay worker runs asynchronously alongside the application lifecycle. To claim pending outbox events without row-lock contention across concurrent instances, the worker uses an atomic PostgreSQL claim query: `UPDATE audit_outbox SET available_at = $1 WHERE event_id IN (SELECT event_id FROM audit_outbox WHERE delivery_state = 'PENDING' AND available_at <= $2 ORDER BY available_at ASC, occurred_at ASC LIMIT $3 FOR UPDATE SKIP LOCKED) RETURNING ...`. Claimed events are projected into `audit_logs` keyed by `source_event_id` (`ON CONFLICT (source_event_id) DO NOTHING`). Outbox staging entries are marked `DELIVERED` only after successful projection. To allow outbox staging cleanup (`CleanupDelivered` pruning records > 30 days) without deleting or cascading historical audit logs, `audit_logs.source_event_id` is intentionally decoupled from DB-level foreign key constraints. A background reconciliation ticker periodically monitors relay lag (>5 min warning, >15 min critical) and triggers exponential backoff retries on failure.

Operational metrics are collected via a centralized, dependency-injected `MetricsCollector` without global singletons or external APM vendor SDKs. It tracks outbox backlog lag, idempotency outcomes (`acquired`, `replayed`, `conflict`), finalization conflicts, HTTP request latencies, and PostgreSQL connection pool stats. The `/metrics` endpoint is protected by constant-time header-token authentication (`X-Metrics-Token`) and returns `Cache-Control: no-store, no-cache, must-revalidate` to prevent upstream caching. Authentication routes (`/auth/login` and `/auth/refresh`) are protected by an in-memory IP rate limiter (max 10 requests/minute, returning `429 TOO_MANY_REQUESTS` with `Retry-After: 60`). Structured access logging incorporates expanded PII redaction for `email`, `phone`, `tax_id`, `bank_account`, `password`, `token`, and `authorization` headers. Trusted proxy parsing (`SetTrustedProxies`) is configured as fail-closed.

System correctness under real PostgreSQL concurrency is verified by a dedicated PostgreSQL Release Gate suite (`internal/integration/postgres_release_gate_test.go`) and an HTTP Release Contract suite (`internal/httpapi/release_contract_test.go`). The PostgreSQL Release Gate validates full database migration roll-up, tear-down, and re-application (`UpDownUp`), single-winner idempotency claim acquisition under parallel goroutines (`TestPostgreSQLReleaseGate_IdempotencyClaimHasSingleWinner`), single disbursement creation & outbox event insertion under N-way concurrent requests, atomic finalization status locking, and atomic refresh token rotation. To prevent accidental execution against non-test or remote databases, integration tests strictly enforce loopback host targets (`localhost`/`127.0.0.1`), dedicated database names (`disbursement_api_release_gate_test`), and destructive schema safety guards (`assertFreshReleaseGateDatabase`).

The production `Dockerfile` uses a multi-stage build: `golang:1.21-alpine` compiles a statically-linked CGO-disabled binary (`-ldflags="-w -s"`), which is copied into a minimal `alpine:3.19` runtime environment running under an unprivileged non-root user (`appuser:appgroup`). Automated CI (`.github/workflows/ci.yml`) triggers on `push` and `pull_request` to `master`/`main`, provisioning an isolated `postgres:15-alpine` container service, executing linting (`go vet`), running unit and integration test suites with Go's race detector (`go test -v -race ./...`), and validating Docker build reproducibility.

The trade-off of running the relay worker in-process (same binary as the HTTP server) is that a server restart also interrupts relay delivery. This is acceptable because `PENDING` outbox records remain durable in PostgreSQL and are immediately reclaimed on restart without data loss.

## 2. Supporting Decisions and Trade-offs

| Decision | Rationale | Accepted Trade-off |
|---|---|---|
| PostgreSQL-backed refresh rotation | Avoids external caching infrastructure (Redis) while leveraging PostgreSQL row-level locks for atomic single-winner rotation. | Incurs DB write per refresh; acceptable for MVP session volume. |
| SHA-256 token hashing | Plaintext refresh tokens are never stored in DB or logged; DB leakage does not compromise active sessions. | Requires SHA-256 computation on token verification/rotation. |
| Explicit HMAC algorithm validation | Rejects `none` algorithm and asymmetric RSA key-confusion attacks in JWT middleware. | Requires HMAC-HS256 key configuration across API services. |
| Transactional audit outbox | Durable audit events commit atomically alongside create/finalize/delete; projection relay can retry after process crashes. | Incurs additional write and worker processing; `GET /audit-logs` is eventually consistent when the relay lags. |
| Atomic outbox locking (`SKIP LOCKED`) | Prevents duplicate pickup across concurrent worker instances without locking the entire outbox table. | Requires PostgreSQL 9.5+ row-level lock support. |
| Decoupled `audit_logs.source_event_id` FK | Allows 30-day outbox staging cleanup (`CleanupDelivered`) without deleting or cascading historical `audit_logs`. | Schema integrity relies on application-level `ON CONFLICT DO NOTHING` projections. |
| Header-based `/metrics` token auth | Protects operational metrics against unauthorized reconnaissance without complex OAuth infrastructure. | Requires secure configuration of `METRICS_TOKEN` environment variable. |
| In-memory IP rate limiter | Protects `/auth/login` and `/auth/refresh` from brute-force credential stuffing without Redis overhead. | Rate limit counters are local to each application process instance. |
| Dedicated PostgreSQL Release Gate Harness | Empirically verifies schema migration `UpDownUp`, single-winner concurrency claims, and single outbox events on real PostgreSQL. | Requires local PostgreSQL or CI disposable container service when environment authorization is enabled. |
| Multi-stage unprivileged Docker container | Minimizes image footprint (`alpine:3.19`) and prevents root container privilege escalation. | Requires non-root file permission management during container build. |
| Unique `audit_logs.source_event_id` | Relay retries will not generate duplicate audit log entries. | Schema and relay worker require delivery-state tracking logic. |
| Semantic JSON replay | HTTP status and JSON payload structure match semantically without enforcing whitespace or key-ordering identity. | Does not satisfy byte-for-byte response identity, which is not required by the contract. |
| Soft delete + `204` repeat delete | Transactional data remains accessible for audit purposes; repeat deletes are safe and do not generate duplicate audit events. | Read queries must explicitly exclude `deleted_at` and distinguish deleted items from non-existent IDs. |
| `decided_by` as canonical actor | Accurately represents the finalizer actor for both `APPROVED` and `REJECTED` states. | If legacy evaluators require `approved_by`, provide a read-only alias; do not store duplicate actor columns. |
| UTC half-open date range | List and audit filtering predicates remain consistent and independent of timestamp precision. | Clients must supply date filter parameters in UTC for MVP. |

## 3. Implementation Invariants

- Create, finalization, and first delete mutations must always write a durable outbox event within the exact same business database transaction.
- Idempotency completion must always include the `claim_id` owner predicate; a completion query resulting in zero affected rows must never be treated as success.
- Finalization and soft-delete must execute via SQL update predicates, never via status checks separated from the write operation.
- The relay worker marks an outbox event `DELIVERED` only after idempotent projection into `audit_logs` succeeds.
- Outbox staging retention cleanup must never cascade or delete historical records in `audit_logs`.
- Protected endpoints (such as `/metrics`) must reject missing or mismatched authentication tokens with `401 Unauthorized` and emit `Cache-Control: no-store`.
- HTTP handlers must not execute raw SQL, manage database transactions, or evaluate business rules; handlers only decode/validate request shapes, delegate to service boundaries, and map domain errors to HTTP envelopes.
- Release-gate integration tests must enforce loopback host targets (`localhost`/`127.0.0.1`), dedicated database names (`disbursement_api_release_gate_test`), and fresh public schema validation before executing destructive migration tests.

## 4. Mandatory Proof Criteria Before Completion

- Simultaneous N-way create requests with identical key/payload yield exactly one disbursement record; stale owners and expired-key reclaims cannot produce duplicates.
- Simultaneous N-way finalization requests yield exactly one winning request; finalizer actor and status cannot be overwritten.
- Outbox transaction failures trigger complete business mutation rollbacks; relay crashes or retries produce at most one audit log entry per `source_event_id`.
- Repeat delete requests do not modify `updated_at` timestamps or emit duplicate audit outbox events.
- PostgreSQL schema migrations pass `up → down → up`; `go test -v -race ./...` and `go vet ./...` pass cleanly with zero race conditions.
- Dedicated PostgreSQL release-gate integration tests verify single-winner idempotency claim acquisition, single outbox event insertion, and atomic refresh token rotation.
- Docker build smoke test (`docker build -t disbursement-api:latest .`) succeeds and binary runs under unprivileged user `appuser`.

## 5. MVP Boundaries

This implementation does not claim live payment settlement integration, availability SLO guarantees, database backup/PITR, RPO/RTO SLAs, or legal audit retention policies. Maximum transaction amount limits and outbox retention policies represent MVP baselines; production rollout requires review and sign-off from Finance, Product, and Operations.
