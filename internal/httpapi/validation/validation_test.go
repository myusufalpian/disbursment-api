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

type sampleValidationStruct struct {
	NumericField  string `json:"numeric_field" validate:"numeric"`
	AlphaNumField string `json:"alphanum_field" validate:"alphanum"`
	MinField      string `json:"min_field" validate:"min=5"`
	OneOfField    string `json:"oneof_field" validate:"oneof=A B"`
	DateTimeField string `json:"datetime_field" validate:"datetime=2006-01-02"`
	GTEField      int    `json:"gte_field" validate:"gte=10"`
	LTEField      int    `json:"lte_field" validate:"lte=50"`
	EmailField    string `json:"email_field" validate:"email"`
}

func TestValidateAllMessageForTags(t *testing.T) {
	checker, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	invalidSample := sampleValidationStruct{
		NumericField:  "abc",
		AlphaNumField: "abc#123",
		MinField:      "a",
		OneOfField:    "C",
		DateTimeField: "2026/01/01",
		GTEField:      5,
		LTEField:      100,
		EmailField:    "not-an-email",
	}

	details := checker.Validate(invalidSample)
	if len(details) == 0 {
		t.Fatalf("expected validation errors, got 0")
	}

	messages := make(map[string]string)
	for _, detail := range details {
		messages[detail.Field] = detail.Message
	}

	if messages["numeric_field"] != "harus berupa angka" {
		t.Errorf("numeric_field = %q, want harus berupa angka", messages["numeric_field"])
	}
	if messages["alphanum_field"] != "harus alfanumerik" {
		t.Errorf("alphanum_field = %q, want harus alfanumerik", messages["alphanum_field"])
	}
	if messages["min_field"] != "panjang minimum 5" {
		t.Errorf("min_field = %q, want panjang minimum 5", messages["min_field"])
	}
	if messages["oneof_field"] != "nilai tidak didukung" {
		t.Errorf("oneof_field = %q, want nilai tidak didukung", messages["oneof_field"])
	}
	if messages["datetime_field"] != "format tanggal tidak valid" {
		t.Errorf("datetime_field = %q, want format tanggal tidak valid", messages["datetime_field"])
	}
	if messages["gte_field"] != "harus lebih besar atau sama dengan 10" {
		t.Errorf("gte_field = %q, want harus lebih besar atau sama dengan 10", messages["gte_field"])
	}
	if messages["lte_field"] != "harus lebih kecil atau sama dengan 50" {
		t.Errorf("lte_field = %q, want harus lebih kecil atau sama dengan 50", messages["lte_field"])
	}
	if messages["email_field"] != "tidak valid" {
		t.Errorf("email_field = %q, want tidak valid", messages["email_field"])
	}
}
