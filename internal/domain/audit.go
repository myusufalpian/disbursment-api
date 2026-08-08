package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"disbursment-api/internal/sensitivity"

	"github.com/google/uuid"
)

const redactedAuditValue = "[REDACTED]"

type AuditEvent struct {
	EventID    uuid.UUID
	EntityType string
	EntityID   uuid.UUID
	Action     string
	ActorID    uuid.UUID
	RequestID  uuid.UUID
	BeforeData json.RawMessage
	AfterData  json.RawMessage
	OccurredAt time.Time
}

func NewAuditEvent(eventID uuid.UUID, entityType string, entityID uuid.UUID, action string, actorID uuid.UUID, requestID uuid.UUID, before any, after any, occurredAt time.Time) (AuditEvent, error) {
	if eventID == uuid.Nil || entityID == uuid.Nil || actorID == uuid.Nil || requestID == uuid.Nil || strings.TrimSpace(entityType) == "" || strings.TrimSpace(action) == "" || occurredAt.IsZero() {
		return AuditEvent{}, fmt.Errorf("invalid audit event")
	}
	beforeData, err := AuditSnapshot(before)
	if err != nil {
		return AuditEvent{}, err
	}
	afterData, err := AuditSnapshot(after)
	if err != nil {
		return AuditEvent{}, err
	}
	return AuditEvent{
		EventID:    eventID,
		EntityType: entityType,
		EntityID:   entityID,
		Action:     action,
		ActorID:    actorID,
		RequestID:  requestID,
		BeforeData: beforeData,
		AfterData:  afterData,
		OccurredAt: occurredAt.UTC(),
	}, nil
}

func AuditSnapshot(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal audit snapshot: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode audit snapshot: %w", err)
	}
	redacted, err := json.Marshal(redactAuditValue(decoded))
	if err != nil {
		return nil, fmt.Errorf("encode audit snapshot: %w", err)
	}
	return json.RawMessage(redacted), nil
}

func redactAuditValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, nested := range typed {
			if sensitivity.IsSensitiveKey(key) {
				redacted[key] = redactedAuditValue
				continue
			}
			redacted[key] = redactAuditValue(nested)
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, nested := range typed {
			redacted[index] = redactAuditValue(nested)
		}
		return redacted
	default:
		return value
	}
}
