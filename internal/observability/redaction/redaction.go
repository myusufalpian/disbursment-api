package redaction

import (
	"log/slog"

	"disbursment-api/internal/sensitivity"
)

const redactedValue = "[REDACTED]"

func Attribute(attribute slog.Attr) slog.Attr {
	if sensitivity.IsSensitiveKey(attribute.Key) {
		return slog.String(attribute.Key, redactedValue)
	}
	return attribute
}

func Attributes(attributes ...slog.Attr) []slog.Attr {
	redacted := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		redacted = append(redacted, Attribute(attribute))
	}
	return redacted
}

func SensitiveValue(value string) string {
	if value == "" {
		return ""
	}
	return redactedValue
}
