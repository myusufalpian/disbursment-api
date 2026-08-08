package validation

import (
	"strings"
	"testing"

	"disbursment-api/internal/httpapi/dto"
)

func TestValidateCountsUnicodeRunesForMaxChars(t *testing.T) {
	checker, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	valid := validCreateRequest(strings.Repeat("😀", 150))
	if details := checker.Validate(valid); len(details) != 0 {
		t.Fatalf("Validate() details = %#v, want no errors for 150 Unicode characters", details)
	}

	invalid := validCreateRequest(strings.Repeat("😀", 151))
	details := checker.Validate(invalid)

	if len(details) != 1 {
		t.Fatalf("Validate() error count = %d, want 1; details = %#v", len(details), details)
	}
	if details[0].Field != "recipient_name" {
		t.Errorf("Validate() field = %q, want recipient_name", details[0].Field)
	}
	if details[0].Message != "panjang maksimum 150" {
		t.Errorf("Validate() message = %q, want panjang maksimum 150", details[0].Message)
	}
}

func TestValidateMapsRequiredFieldsToIndonesianMessages(t *testing.T) {
	checker, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	details := checker.Validate(dto.LoginRequest{})
	messages := make(map[string]string, len(details))
	for _, detail := range details {
		messages[detail.Field] = detail.Message
	}

	if len(messages) != 2 {
		t.Fatalf("Validate() error count = %d, want 2; details = %#v", len(messages), details)
	}
	if messages["username"] != "wajib diisi" {
		t.Errorf("username message = %q, want wajib diisi", messages["username"])
	}
	if messages["password"] != "wajib diisi" {
		t.Errorf("password message = %q, want wajib diisi", messages["password"])
	}
}

func validCreateRequest(recipientName string) dto.CreateDisbursementRequest {
	return dto.CreateDisbursementRequest{
		RecipientName: recipientName,
		AccountNumber: "123456",
		BankCode:      "BCA",
		Amount:        10000,
	}
}
