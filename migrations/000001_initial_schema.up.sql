CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TYPE user_role AS ENUM ('OPERATOR', 'ADMIN', 'SUPERADMIN');
CREATE TYPE disbursement_status AS ENUM ('PENDING', 'APPROVED', 'REJECTED');
CREATE TYPE idempotency_state AS ENUM ('IN_PROGRESS', 'COMPLETED');
CREATE TYPE outbox_delivery_state AS ENUM ('PENDING', 'DELIVERED');

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(100) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role user_role NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE refresh_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    replaced_by_id UUID UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT refresh_sessions_expiry_check CHECK (expires_at > created_at)
);

CREATE TABLE disbursements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_name VARCHAR(150) NOT NULL,
    account_number VARCHAR(34) NOT NULL,
    bank_code VARCHAR(10) NOT NULL,
    amount BIGINT NOT NULL,
    admin_fee INTEGER NOT NULL,
    status disbursement_status NOT NULL DEFAULT 'PENDING',
    note VARCHAR(500),
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    decided_by UUID REFERENCES users(id) ON DELETE RESTRICT,
    decision_note VARCHAR(500),
    decided_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT disbursements_account_number_check CHECK (account_number ~ '^[0-9]{6,34}$'),
    CONSTRAINT disbursements_bank_code_check CHECK (bank_code = upper(bank_code) AND bank_code ~ '^[A-Z0-9]{3,10}$'),
    CONSTRAINT disbursements_amount_check CHECK (amount BETWEEN 10000 AND 100000000000),
    CONSTRAINT disbursements_admin_fee_check CHECK (
        (amount < 5000000 AND admin_fee = 2500) OR
        (amount >= 5000000 AND admin_fee = 5000)
    ),
    CONSTRAINT disbursements_decision_check CHECK (
        (status = 'PENDING' AND decided_by IS NULL AND decided_at IS NULL) OR
        (status IN ('APPROVED', 'REJECTED') AND decided_by IS NOT NULL AND decided_at IS NOT NULL)
    ),
    CONSTRAINT disbursements_deleted_status_check CHECK (deleted_at IS NULL OR status = 'PENDING')
);

CREATE TABLE idempotency_keys (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    endpoint VARCHAR(128) NOT NULL,
    key UUID NOT NULL,
    request_hash BYTEA NOT NULL,
    state idempotency_state NOT NULL,
    claim_id UUID NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    disbursement_id UUID REFERENCES disbursements(id) ON DELETE RESTRICT,
    response_status SMALLINT,
    response_body JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, endpoint, key),
    CONSTRAINT idempotency_keys_lease_check CHECK (lease_until <= expires_at),
    CONSTRAINT idempotency_keys_completed_response_check CHECK (
        (state = 'IN_PROGRESS' AND response_status IS NULL AND response_body IS NULL) OR
        (state = 'COMPLETED' AND response_status BETWEEN 200 AND 299 AND response_body IS NOT NULL)
    )
);

CREATE TABLE audit_outbox (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_type VARCHAR(64) NOT NULL,
    entity_id UUID NOT NULL,
    action VARCHAR(64) NOT NULL,
    actor_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id UUID NOT NULL,
    before_data JSONB,
    after_data JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivery_state outbox_delivery_state NOT NULL DEFAULT 'PENDING',
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    delivery_attempts INTEGER NOT NULL DEFAULT 0,
    last_delivery_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_outbox_delivery_check CHECK (
        (delivery_state = 'PENDING' AND delivered_at IS NULL) OR
        (delivery_state = 'DELIVERED' AND delivered_at IS NOT NULL)
    ),
    CONSTRAINT audit_outbox_attempts_check CHECK (delivery_attempts >= 0)
);

-- RETENTION ARCHITECTURE DESIGN:
-- audit_outbox is a transactional staging table for event-driven relay delivery.
-- audit_logs is the immutable, long-term queryable historical audit projection table.
-- source_event_id stores the outbox event UUID as an origin tracking identifier without a database FK constraint.
-- This decouples audit_logs retention from audit_outbox cleanup (CleanupDelivered), ensuring historical
-- audit logs are preserved forever when outbox staging records >30 days are pruned.
CREATE TABLE audit_logs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_event_id UUID NOT NULL UNIQUE,
    entity_type VARCHAR(64) NOT NULL,
    entity_id UUID NOT NULL,
    action VARCHAR(64) NOT NULL,
    actor_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    request_id UUID NOT NULL,
    before_data JSONB,
    after_data JSONB,
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION prevent_audit_outbox_event_id_change() RETURNS trigger AS $$
BEGIN
    IF NEW.event_id <> OLD.event_id THEN
        RAISE EXCEPTION 'audit_outbox.event_id is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_outbox_event_id_immutable
BEFORE UPDATE ON audit_outbox
FOR EACH ROW EXECUTE FUNCTION prevent_audit_outbox_event_id_change();

CREATE INDEX disbursements_active_created_at_idx
    ON disbursements (created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX disbursements_active_status_created_at_idx
    ON disbursements (status, created_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX disbursements_active_recipient_name_trgm_idx
    ON disbursements USING GIN (recipient_name gin_trgm_ops)
    WHERE deleted_at IS NULL;
CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);
CREATE INDEX audit_outbox_pending_idx
    ON audit_outbox (available_at, occurred_at)
    WHERE delivery_state = 'PENDING';
CREATE INDEX audit_outbox_delivered_at_idx
    ON audit_outbox (delivered_at)
    WHERE delivery_state = 'DELIVERED';
CREATE INDEX audit_logs_entity_occurred_at_idx ON audit_logs (entity_id, occurred_at DESC);
CREATE INDEX audit_logs_occurred_at_idx ON audit_logs (occurred_at DESC);
