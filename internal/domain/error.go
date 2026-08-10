package domain

import (
	"errors"
	"time"
)

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
	Code       ErrorCode
	Message    string
	Status     int
	Details    []FieldError
	RetryAfter time.Duration
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

func InvalidCredentials() *Error {
	return &Error{
		Code:    CodeInvalidCredentials,
		Message: "Kredensial tidak valid",
		Status:  401,
	}
}

func InvalidIdempotencyKey() *Error {
	return &Error{
		Code:    CodeInvalidIdempotencyKey,
		Message: "Header Idempotency-Key tidak valid atau tidak ada",
		Status:  400,
	}
}

func Unauthorized() *Error {
	return &Error{
		Code:    CodeUnauthorized,
		Message: "Autentikasi diperlukan",
		Status:  401,
	}
}

func InvalidRefreshToken() *Error {
	return &Error{
		Code:    CodeInvalidRefreshToken,
		Message: "Refresh token tidak valid",
		Status:  401,
	}
}

func Forbidden() *Error {
	return &Error{
		Code:    CodeForbidden,
		Message: "Akses ditolak",
		Status:  403,
	}
}

func Internal() *Error {
	return &Error{
		Code:    CodeInternalError,
		Message: "Terjadi kesalahan internal",
		Status:  500,
	}
}

func DisbursementNotFound() *Error {
	return &Error{
		Code:    CodeDisbursementNotFound,
		Message: "Disbursement tidak ditemukan",
		Status:  404,
	}
}

func DisbursementAlreadyFinalized() *Error {
	return &Error{
		Code:    CodeDisbursementAlreadyFinalized,
		Message: "Disbursement sudah difinalisasi",
		Status:  409,
	}
}

func DisbursementNotDeletable() *Error {
	return &Error{
		Code:    CodeDisbursementNotDeletable,
		Message: "Disbursement tidak dapat dihapus karena sudah difinalisasi",
		Status:  400,
	}
}

func ConcurrentModification() *Error {
	return &Error{
		Code:    CodeConcurrentModification,
		Message: "Terjadi konflik modifikasi simultan",
		Status:  409,
	}
}

func IdempotencyKeyReused() *Error {
	return &Error{
		Code:    CodeIdempotencyKeyReused,
		Message: "Idempotency key telah digunakan dengan payload berbeda",
		Status:  409,
	}
}

func IdempotencyRequestInProgress() *Error {
	return &Error{
		Code:    CodeIdempotencyInProgress,
		Message: "Request dengan idempotency key yang sama sedang diproses",
		Status:  409,
	}
}

func IdempotencyRequestInProgressWithRetryAfter(retryAfter time.Duration) *Error {
	err := IdempotencyRequestInProgress()
	err.RetryAfter = retryAfter
	return err
}

func AsError(err error) *Error {
	var domainError *Error
	if errors.As(err, &domainError) {
		return domainError
	}
	return Internal()
}
