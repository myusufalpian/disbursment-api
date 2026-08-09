package response

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"disbursment-api/internal/domain"
)

type Meta struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type Success struct {
	Success bool  `json:"success"`
	Data    any   `json:"data"`
	Meta    *Meta `json:"meta,omitempty"`
}

type Error struct {
	Success   bool   `json:"success"`
	Error     Body   `json:"error"`
	RequestID string `json:"request_id"`
}

type Body struct {
	Code    domain.ErrorCode    `json:"code"`
	Message string              `json:"message"`
	Details []domain.FieldError `json:"details,omitempty"`
}

func WriteSuccess(writer http.ResponseWriter, status int, data any, meta *Meta) {
	writeJSON(writer, status, Success{Success: true, Data: data, Meta: meta})
}

func WriteNoContent(writer http.ResponseWriter) {
	writer.WriteHeader(http.StatusNoContent)
}

func WriteError(writer http.ResponseWriter, requestID string, err error) {
	domainError := domain.AsError(err)
	if domainError.RetryAfter > 0 {
		seconds := int64(domainError.RetryAfter / time.Second)
		if domainError.RetryAfter%time.Second != 0 {
			seconds++
		}
		writer.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	}
	writeJSON(writer, domainError.Status, Error{
		Success: false,
		Error: Body{
			Code:    domainError.Code,
			Message: domainError.Message,
			Details: domainError.Details,
		},
		RequestID: requestID,
	})
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}
