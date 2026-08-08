package domain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAuditSnapshotRecursivelyRedactsSensitiveValues(t *testing.T) {
	snapshot := map[string]any{
		"amount":   1_250_000,
		"password": "plain-password",
		"nested": map[string]any{
			"token": "access-token",
			"items": []any{
				map[string]any{
					"authorization":  "Bearer secret",
					"account_number": "123456",
				},
			},
		},
	}

	encoded, err := AuditSnapshot(snapshot)
	if err != nil {
		t.Fatalf("AuditSnapshot() error = %v", err)
	}

	decoded := decodeAuditJSON(t, encoded)
	root, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("AuditSnapshot() decoded type = %T, want object", decoded)
	}
	assertRedactedAuditValue(t, root, "password")
	assertAuditAmount(t, root, "amount", "1250000")

	nested, ok := root["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested value type = %T, want object", root["nested"])
	}
	assertRedactedAuditValue(t, nested, "token")

	items, ok := nested["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("nested items = %#v, want one item", nested["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("nested item type = %T, want object", items[0])
	}
	assertRedactedAuditValue(t, item, "authorization")
	assertRedactedAuditValue(t, item, "account_number")
}

func TestAuditSnapshotHandlesNil(t *testing.T) {
	snapshot, err := AuditSnapshot(nil)
	if err != nil {
		t.Fatalf("AuditSnapshot(nil) error = %v", err)
	}
	if snapshot != nil {
		t.Errorf("AuditSnapshot(nil) = %s, want nil", snapshot)
	}
}

func TestNewAuditEventConstructsUTCEvent(t *testing.T) {
	eventID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440010")
	entityID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440011")
	actorID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440012")
	requestID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440013")
	occurredAt := time.Date(2026, time.August, 8, 15, 2, 44, 0, time.FixedZone("WIB", 7*60*60))

	event, err := NewAuditEvent(
		eventID,
		"disbursement",
		entityID,
		"created",
		actorID,
		requestID,
		map[string]any{"password": "before-secret"},
		map[string]any{"amount": 10_000},
		occurredAt,
	)
	if err != nil {
		t.Fatalf("NewAuditEvent() error = %v", err)
	}
	if event.EventID != eventID || event.EntityID != entityID || event.ActorID != actorID || event.RequestID != requestID {
		t.Errorf("NewAuditEvent() IDs = %#v, want supplied IDs", event)
	}
	if event.Action != "created" || event.EntityType != "disbursement" {
		t.Errorf("NewAuditEvent() identity = %q/%q, want created/disbursement", event.Action, event.EntityType)
	}
	if !event.OccurredAt.Equal(occurredAt.UTC()) || event.OccurredAt.Location() != time.UTC {
		t.Errorf("OccurredAt = %s (%s), want UTC %s", event.OccurredAt, event.OccurredAt.Location(), occurredAt.UTC())
	}
	if string(event.BeforeData) != `{"password":"[REDACTED]"}` {
		t.Errorf("BeforeData = %s, want redacted snapshot", event.BeforeData)
	}
	if string(event.AfterData) != `{"amount":10000}` {
		t.Errorf("AfterData = %s, want numeric amount snapshot", event.AfterData)
	}
}

func TestNewAuditEventRejectsNilUUIDAndBlankAction(t *testing.T) {
	validEventID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440020")
	validEntityID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440021")
	validActorID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440022")
	validRequestID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440023")
	occurredAt := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		eventID   uuid.UUID
		entityID  uuid.UUID
		actorID   uuid.UUID
		requestID uuid.UUID
		action    string
	}{
		{name: "nil event ID", eventID: uuid.Nil, entityID: validEntityID, actorID: validActorID, requestID: validRequestID, action: "created"},
		{name: "nil entity ID", eventID: validEventID, entityID: uuid.Nil, actorID: validActorID, requestID: validRequestID, action: "created"},
		{name: "nil actor ID", eventID: validEventID, entityID: validEntityID, actorID: uuid.Nil, requestID: validRequestID, action: "created"},
		{name: "nil request ID", eventID: validEventID, entityID: validEntityID, actorID: validActorID, requestID: uuid.Nil, action: "created"},
		{name: "blank action", eventID: validEventID, entityID: validEntityID, actorID: validActorID, requestID: validRequestID, action: "  "},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAuditEvent(test.eventID, "disbursement", test.entityID, test.action, test.actorID, test.requestID, nil, nil, occurredAt)
			if err == nil {
				t.Fatal("NewAuditEvent() error = nil, want error")
			}
		})
	}
}

func decodeAuditJSON(t *testing.T, encoded json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode audit snapshot: %v", err)
	}
	return decoded
}

func assertRedactedAuditValue(t *testing.T, object map[string]any, key string) {
	t.Helper()
	if got := object[key]; got != redactedAuditValue {
		t.Errorf("snapshot[%q] = %#v, want %q", key, got, redactedAuditValue)
	}
}

func assertAuditAmount(t *testing.T, object map[string]any, key, want string) {
	t.Helper()
	got, ok := object[key].(json.Number)
	if !ok || got.String() != want {
		t.Errorf("snapshot[%q] = %#v, want JSON number %s", key, object[key], want)
	}
}
