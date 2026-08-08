# Architecture — Disbursement API v2

This document summarizes the architectural decisions and system design for Disbursement API v2. The target implementation stack consists of Go 1.21, Gin, PostgreSQL, and sqlx. PostgreSQL serves as the sole transaction boundary for business state, idempotency fencing, and durable event audit outbox logging.

## 1.1 Idempotency for `POST /disbursements`

For `POST /disbursements`, I use an `idempotency_keys` table in PostgreSQL as a durable state that is transactionally committed alongside the disbursement resource. Idempotency keys are scoped by `(user_id, endpoint, key)` to isolate keys across different users. Before claiming a key, the API validates the UUID v4 header format and full request body payload; validation failures do not create an idempotency record. A request fingerprint is computed from the HTTP method, route, and canonical JSON payload. The record stores the fingerprint, state (`IN_PROGRESS` or `COMPLETED`), a random `claim_id` UUID, lease expiration timestamp, 24-hour replay window, HTTP response status, and JSON response body.

The initial request executes an atomic claim. Requests with a differing payload fingerprint receive `409 IDEMPOTENCY_KEY_REUSED`; active concurrent claims receive `409` with a `Retry-After` header; unexpired `COMPLETED` records replay the semantically equivalent status and JSON body along with an `X-Idempotent-Replayed: true` header. Stale active claims or expired records can be atomically reclaimed with a new `claim_id`. However, a former claim owner is never trusted simply because it once acquired the key: the business transaction locks the idempotency row using `SELECT ... FOR UPDATE` and verifies ownership of the `claim_id` before executing the resource insertion.

Within the same database transaction, the service creates the disbursement record, marks the idempotency claim completed using the `claim_id` owner predicate, and writes a durable audit outbox event. If any step fails, the entire transaction rolls back. This approach prevents duplicate resource creation even if the initial HTTP response is lost or if a delayed request resumes after lease reclamation. The trade-off is a more complex state machine and PostgreSQL concurrency testing compared to in-memory caches or Redis, but neither can commit atomically with business data without additional fencing mechanisms.

## 1.2 Concurrency and Locking for Approval

For approval or rejection, I use a SQL conditional update in PostgreSQL rather than an application-side read-then-write approach. A single SQL statement atomically evaluates business preconditions: the target resource must exist with the correct ID, be in `PENDING` status, and must not be soft-deleted. For example: `UPDATE disbursements ... WHERE id = $1 AND status = 'PENDING' AND deleted_at IS NULL RETURNING *`. A single returned row signifies that the request successfully won the finalization race; `decided_by`, status, decision note, and `updated_at` are persisted together. The returned row serves as the before/after snapshot for the audit outbox event in the same transaction.

If zero rows are returned by the update statement, the service performs a controlled diagnostic read. A non-existent or soft-deleted resource yields `404 DISBURSEMENT_NOT_FOUND`; a resource already in `APPROVED` or `REJECTED` state yields `409 DISBURSEMENT_ALREADY_FINALIZED`; a request losing the race due to simultaneous execution yields `409 CONCURRENT_MODIFICATION`. Under PostgreSQL `READ COMMITTED` isolation level, the losing update waits briefly for the row lock held by the first update, then re-evaluates the `status = 'PENDING'` predicate after commit. Because the status has transitioned to final, the second update affects zero rows. Lost updates are eliminated without requiring distributed locks.

The trade-off is that losing requests may wait briefly for a row lock and require an additional diagnostic read to yield an informative error response. This remains significantly simpler and safer than using `SELECT ... FOR UPDATE` as the primary mechanism—which holds locks longer and moves critical precondition checks into application code. Version-based optimistic locking was not selected because clients do not supply version tokens; Redis/distributed locks introduce additional failure modes without offering stronger authority than the database holding the transaction state.

## Supporting Decisions and Trade-offs

| Decision | Rationale | Accepted Trade-off |
|---|---|---|
| Transactional audit outbox | Durable audit events commit atomically alongside create/finalize/delete; projection relay can retry after process crashes. | Incurs additional write and worker processing; `GET /audit-logs` is eventually consistent when the relay lags. |
| Unique `audit_logs.source_event_id` | Relay retries will not generate duplicate audit log entries. | Schema and relay worker require delivery-state tracking logic. |
| Semantic JSON replay | HTTP status and JSON payload structure match semantically without enforcing whitespace or key-ordering identity. | Does not satisfy byte-for-byte response identity, which is not required by the MVP contract. |
| Soft delete + `204` repeat delete | Transactional data remains accessible for audit purposes; repeat deletes are safe and do not generate duplicate audit events. | Read queries must explicitly exclude `deleted_at` and distinguish deleted items from non-existent IDs. |
| `decided_by` as canonical actor | Accurately represents the finalizer actor for both `APPROVED` and `REJECTED` states. | If legacy evaluators require `approved_by`, provide a read-only alias; do not store duplicate actor columns. |
| UTC half-open date range | List and audit filtering predicates remain consistent and independent of timestamp precision. | Clients must supply date filter parameters in UTC for MVP. |

## Implementation Invariants

- Create, finalization, and first delete mutations must always write a durable outbox event within the exact same business database transaction.
- Idempotency completion must always include the `claim_id` owner predicate; a completion query resulting in zero affected rows must never be treated as success.
- Finalization and soft-delete must execute via SQL update predicates, never via status checks separated from the write operation.
- The relay worker marks an outbox event `DELIVERED` only after idempotent projection into `audit_logs` succeeds.
- HTTP handlers must not execute raw SQL, manage database transactions, or evaluate business rules; handlers only decode/validate request shapes, delegate to service boundaries, and map domain errors to HTTP envelopes.

## Mandatory Proof Criteria Before Completion

- Simultaneous N-way create requests with identical key/payload yield exactly one disbursement record; stale owners and expired-key reclaims cannot produce duplicates.
- Simultaneous N-way finalization requests yield exactly one winning request; finalizer actor and status cannot be overwritten.
- Outbox transaction failures trigger complete business mutation rollbacks; relay crashes or retries produce at most one audit log entry per `source_event_id`.
- Repeat delete requests do not modify `updated_at` timestamps or emit duplicate audit outbox events.
- PostgreSQL schema migrations pass `up → down → up`; `go test ./...` and `go vet ./...` pass cleanly.

## MVP Boundaries

This implementation does not claim live payment settlement integration, availability SLO guarantees, database backup/PITR, RPO/RTO SLAs, or legal audit retention policies. Maximum transaction amount limits and outbox retention policies represent MVP baselines; production rollout requires review and sign-off from Finance, Product, and Operations.
