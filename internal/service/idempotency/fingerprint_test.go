package idempotency

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
)

func TestFingerprintCanonicalizesEquivalentRequests(t *testing.T) {
	first, err := Fingerprint("POST", "/disbursements", json.RawMessage(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("Fingerprint() first error = %v", err)
	}

	second, err := Fingerprint(" post ", " /disbursements ", json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("Fingerprint() second error = %v", err)
	}

	if first != second {
		t.Fatalf("Fingerprint() for equivalent requests differs: first = %x, second = %x", first, second)
	}
}

func TestFingerprintDiffersForChangedRequestParts(t *testing.T) {
	baseline, err := Fingerprint("POST", "/disbursements", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("Fingerprint() baseline error = %v", err)
	}

	tests := []struct {
		name     string
		method   string
		endpoint string
		payload  any
	}{
		{name: "method", method: "GET", endpoint: "/disbursements", payload: json.RawMessage(`{"a":1}`)},
		{name: "endpoint", method: "POST", endpoint: "/payments", payload: json.RawMessage(`{"a":1}`)},
		{name: "payload", method: "POST", endpoint: "/disbursements", payload: json.RawMessage(`{"a":2}`)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fingerprint, err := Fingerprint(test.method, test.endpoint, test.payload)
			if err != nil {
				t.Fatalf("Fingerprint() error = %v", err)
			}
			if fingerprint == baseline {
				t.Errorf("Fingerprint() = %x, want a value different from baseline %x", fingerprint, baseline)
			}
		})
	}
}

func TestFingerprintRejectsBlankMethodOrEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		endpoint string
	}{
		{name: "blank method", method: "", endpoint: "/disbursements"},
		{name: "whitespace method", method: "  ", endpoint: "/disbursements"},
		{name: "blank endpoint", method: "POST", endpoint: ""},
		{name: "whitespace endpoint", method: "POST", endpoint: "  "},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Fingerprint(test.method, test.endpoint, json.RawMessage(`{"a":1}`))
			if err == nil {
				t.Fatal("Fingerprint() error = nil, want an error")
			}
		})
	}
}

func TestFingerprintRejectsUnmarshalablePayload(t *testing.T) {
	fingerprint, err := Fingerprint("POST", "/disbursements", json.RawMessage(`{"a":`))

	if err == nil {
		t.Fatal("Fingerprint() error = nil, want an error")
	}
	if fingerprint != [sha256.Size]byte{} {
		t.Errorf("Fingerprint() = %x, want the zero fingerprint", fingerprint)
	}
}
