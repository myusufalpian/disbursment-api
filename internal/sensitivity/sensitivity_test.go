package sensitivity

import "testing"

func TestIsSensitiveKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "password", key: "password", want: true},
		{name: "trimmed mixed case password", key: "  PaSsWoRd  ", want: true},
		{name: "authorization", key: "authorization", want: true},
		{name: "mixed case authorization header", key: "  Authorization_Header  ", want: true},
		{name: "account number", key: "account_number", want: true},
		{name: "trimmed mixed case account number", key: "  Recipient_Account_Number  ", want: true},
		{name: "token", key: "token", want: true},
		{name: "mixed case refresh token", key: "Refresh_Token", want: true},
		{name: "request ID", key: "request_id", want: false},
		{name: "method", key: "method", want: false},
		{name: "recipient name", key: "recipient_name", want: false},
		{name: "empty key", key: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSensitiveKey(test.key); got != test.want {
				t.Errorf("IsSensitiveKey(%q) = %t, want %t", test.key, got, test.want)
			}
		})
	}
}
