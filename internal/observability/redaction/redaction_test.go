package redaction

import (
	"log/slog"
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
