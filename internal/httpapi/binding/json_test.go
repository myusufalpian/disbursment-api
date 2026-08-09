package binding

import (
	"strings"
	"testing"
)

type sampleStruct struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestDecodeJSON(t *testing.T) {
	t.Run("valid JSON decode succeeds", func(t *testing.T) {
		input := `{"name":"John","age":30}`
		var dest sampleStruct
		err := DecodeJSON(strings.NewReader(input), &dest)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
		if dest.Name != "John" || dest.Age != 30 {
			t.Fatalf("unexpected struct values: %+v", dest)
		}
	})

	t.Run("disallowed unknown fields returns error", func(t *testing.T) {
		input := `{"name":"John","age":30,"unknown":"field"}`
		var dest sampleStruct
		err := DecodeJSON(strings.NewReader(input), &dest)
		if err == nil {
			t.Fatalf("expected error for unknown field, got nil")
		}
	})

	t.Run("multiple JSON values in body returns error", func(t *testing.T) {
		input := `{"name":"John","age":30}{"name":"Doe"}`
		var dest sampleStruct
		err := DecodeJSON(strings.NewReader(input), &dest)
		if err == nil {
			t.Fatalf("expected error for multiple JSON values, got nil")
		}
	})

	t.Run("malformed JSON body returns error", func(t *testing.T) {
		input := `{"name":"John",`
		var dest sampleStruct
		err := DecodeJSON(strings.NewReader(input), &dest)
		if err == nil {
			t.Fatalf("expected error for malformed JSON, got nil")
		}
	})
}
