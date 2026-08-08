package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func Fingerprint(method string, endpoint string, payload any) ([sha256.Size]byte, error) {
	canonicalPayload, err := canonicalJSON(payload)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	canonicalMethod := strings.ToUpper(strings.TrimSpace(method))
	canonicalEndpoint := strings.TrimSpace(endpoint)
	if canonicalMethod == "" || canonicalEndpoint == "" {
		return [sha256.Size]byte{}, fmt.Errorf("method and endpoint are required for idempotency fingerprint")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(canonicalMethod))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(canonicalEndpoint))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonicalPayload)
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint, nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal idempotency payload: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode idempotency payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("idempotency payload must contain one JSON value")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode idempotency payload: %w", err)
	}
	return canonical, nil
}
