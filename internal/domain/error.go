package domain

import "errors"

type ErrorCode string

const (
	CodeValidationError              ErrorCode = "VALIDATION_ERROR"
	CodeInvalidIdempotencyKey        ErrorCode = "INVALID_IDEMPOTENCY_KEY"
	CodeInvalidCredentials           ErrorCode = "INVALID_CREDENTIALS"
	CodeUnauthorized                 ErrorCode = "UNAUTHORIZED"
	CodeInvalidRefreshToken          ErrorCode = "INVALID_REFRESH_TOKEN"
	CodeForbidden                    ErrorCode = "FORBIDDEN"
	CodeDisbursementNotFound         ErrorCode = "DISBURSEMENT_NOT_FOUND"
	CodeDisbursementAlreadyFinalized ErrorCode = "DISBURSEMENT_ALREADY_FINALIZED"
	CodeDisbursementNotDeletable     ErrorCode = "DISBURSEMENT_NOT_DELETABLE"
	CodeConcurrentModification       ErrorCode = "CONCURRENT_MODIFICATION"
	CodeIdempotencyKeyReused         ErrorCode = "IDEMPOTENCY_KEY_REUSED"
	CodeIdempotencyInProgress        ErrorCode = "IDEMPOTENCY_REQUEST_IN_PROGRESS"
	CodeInternalError                ErrorCode = "INTERNAL_ERROR"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Error struct {
	Code    ErrorCode
	Message string
	Status  int
	Details []FieldError
}

func (e *Error) Error() string {
	return string(e.Code)
}

func Validation(details []FieldError) *Error {
	return &Error{
		Code:    CodeValidationError,
		Message: "Input tidak valid",
		Status:  400,
		Details: details,
	}
}

func Internal() *Error {
	return &Error{
		Code:    CodeInternalError,
		Message: "Terjadi kesalahan internal",
		Status:  500,
	}
}

func AsError(err error) *Error {
	var domainError *Error
	if errors.As(err, &domainError) {
		return domainError
	}
	return Internal()
}
