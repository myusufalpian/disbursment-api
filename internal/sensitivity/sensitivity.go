package sensitivity

import "strings"

func IsSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "account_number") ||
		strings.Contains(normalized, "token")
}
