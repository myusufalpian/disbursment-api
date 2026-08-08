package validation

import (
	"reflect"
	"strings"
	"unicode/utf8"

	"disbursment-api/internal/domain"

	"github.com/go-playground/validator/v10"
)

type Validator struct {
	engine *validator.Validate
}

func New() (*Validator, error) {
	engine := validator.New()
	engine.RegisterTagNameFunc(jsonFieldName)
	if err := engine.RegisterValidation("maxchars", maxCharacters); err != nil {
		return nil, err
	}
	return &Validator{engine: engine}, nil
}

func (checker *Validator) Validate(value any) []domain.FieldError {
	if err := checker.engine.Struct(value); err != nil {
		validationErrors, ok := err.(validator.ValidationErrors)
		if !ok {
			return []domain.FieldError{{Field: "body", Message: "Input tidak valid"}}
		}
		return toFieldErrors(validationErrors)
	}
	return nil
}

func jsonFieldName(field reflect.StructField) string {
	name := strings.Split(field.Tag.Get("json"), ",")[0]
	if name == "" || name == "-" {
		return field.Name
	}
	return name
}

func maxCharacters(level validator.FieldLevel) bool {
	maximum, ok := parseMaximum(level.Param())
	if !ok || level.Field().Kind() != reflect.String {
		return false
	}
	return utf8.RuneCountInString(level.Field().String()) <= maximum
}

func parseMaximum(value string) (int, bool) {
	maximum := 0
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
		maximum = maximum*10 + int(character-'0')
	}
	return maximum, maximum > 0
}

func toFieldErrors(validationErrors validator.ValidationErrors) []domain.FieldError {
	details := make([]domain.FieldError, 0, len(validationErrors))
	for _, fieldError := range validationErrors {
		details = append(details, domain.FieldError{
			Field:   fieldError.Field(),
			Message: messageFor(fieldError),
		})
	}
	return details
}

func messageFor(fieldError validator.FieldError) string {
	switch fieldError.Tag() {
	case "required":
		return "wajib diisi"
	case "gte":
		return "harus lebih besar atau sama dengan " + fieldError.Param()
	case "lte":
		return "harus lebih kecil atau sama dengan " + fieldError.Param()
	case "min":
		return "panjang minimum " + fieldError.Param()
	case "max", "maxchars":
		return "panjang maksimum " + fieldError.Param()
	case "numeric":
		return "harus berupa angka"
	case "alphanum":
		return "harus alfanumerik"
	case "oneof":
		return "nilai tidak didukung"
	case "datetime":
		return "format tanggal tidak valid"
	default:
		return "tidak valid"
	}
}
