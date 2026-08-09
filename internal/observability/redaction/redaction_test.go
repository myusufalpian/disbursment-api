package redaction

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestAttributesRedactsSensitiveKeys(t *testing.T) {
	attributes := Attributes(
		slog.String("password", "plain-text-password"),
		slog.String("authorization_header", "Bearer access-token"),
		slog.String("refresh_token", "refresh-token"),
		slog.String("recipient_account_number", "123456789012"),
	)

	for _, attribute := range attributes {
		if attribute.Value.String() != redactedValue {
			t.Errorf("Attributes() value for %q = %q, want %q", attribute.Key, attribute.Value.String(), redactedValue)
		}
	}
}

func TestAttributesPreservesNonSensitiveObservabilityFields(t *testing.T) {
	attributes := Attributes(
		slog.String("request_id", "bca91f16-dc07-4c8d-a16d-b8bc2103df98"),
		slog.String("method", "POST"),
		slog.String("path", "/disbursements"),
		slog.Int("status_code", 201),
		slog.Int64("latency_ms", 12),
	)

	for _, attribute := range attributes {
		if attribute.Value.String() == redactedValue {
			t.Errorf("Attributes() redacted non-sensitive key %q", attribute.Key)
		}
	}
}

func TestSensitiveValue(t *testing.T) {
	if got := SensitiveValue(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
	if got := SensitiveValue("secret-data"); got != "[REDACTED]" {
		t.Fatalf("expected [REDACTED], got %q", got)
	}
}

func TestAttributesRedactionSurvivesJSONSerialization(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	secretPassword := "plain-text-password"
	secretToken := "refresh-token"

	logger.LogAttrs(
		context.Background(),
		slog.LevelInfo,
		"request completed",
		Attributes(
			slog.String("password", secretPassword),
			slog.String("refresh_token", secretToken),
			slog.String("request_id", "bca91f16-dc07-4c8d-a16d-b8bc2103df98"),
			slog.String("method", "POST"),
			slog.String("path", "/disbursements"),
			slog.Int("status_code", 201),
			slog.Int64("latency_ms", 12),
		)...,
	)

	serialized := logBuf.String()
	if strings.Contains(serialized, secretPassword) || strings.Contains(serialized, secretToken) {
		t.Fatalf("serialized log contains a secret: %s", serialized)
	}

	var logEntry map[string]any
	if err := json.NewDecoder(&logBuf).Decode(&logEntry); err != nil {
		t.Fatalf("decode serialized log JSON: %v; output: %s", err, serialized)
	}
	if logEntry["password"] != redactedValue {
		t.Fatalf("serialized password = %v, want %q", logEntry["password"], redactedValue)
	}
	if logEntry["refresh_token"] != redactedValue {
		t.Fatalf("serialized refresh token = %v, want %q", logEntry["refresh_token"], redactedValue)
	}

	if requestID, ok := logEntry["request_id"].(string); !ok {
		t.Fatalf("serialized request_id has type %T, want string", logEntry["request_id"])
	} else if requestID != "bca91f16-dc07-4c8d-a16d-b8bc2103df98" {
		t.Fatalf("serialized request_id = %q, want %q", requestID, "bca91f16-dc07-4c8d-a16d-b8bc2103df98")
	}
	if method, ok := logEntry["method"].(string); !ok {
		t.Fatalf("serialized method has type %T, want string", logEntry["method"])
	} else if method != "POST" {
		t.Fatalf("serialized method = %q, want %q", method, "POST")
	}
	if path, ok := logEntry["path"].(string); !ok {
		t.Fatalf("serialized path has type %T, want string", logEntry["path"])
	} else if path != "/disbursements" {
		t.Fatalf("serialized path = %q, want %q", path, "/disbursements")
	}
	if statusCode, ok := logEntry["status_code"].(float64); !ok || statusCode != 201 {
		t.Fatalf("serialized status_code = %v (%T), want JSON number 201", logEntry["status_code"], logEntry["status_code"])
	}
	if latency, ok := logEntry["latency_ms"].(float64); !ok || latency != 12 {
		t.Fatalf("serialized latency_ms = %v (%T), want JSON number 12", logEntry["latency_ms"], logEntry["latency_ms"])
	}
}
