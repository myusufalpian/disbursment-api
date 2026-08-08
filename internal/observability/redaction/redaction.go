package redaction

import (
	"log/slog"
	"strings"
)

const redactedValue = "[REDACTED]"

func Attribute(attribute slog.Attr) slog.Attr {
	if sensitive(attribute.Key) {
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

func sensitive(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "account_number") ||
		strings.Contains(normalized, "token")
}
