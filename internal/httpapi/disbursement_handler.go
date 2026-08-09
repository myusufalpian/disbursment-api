package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/disbursement"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type DisbursementHandler struct {
	disbursementService *disbursement.Service
	validator           *validation.Validator
}

func NewDisbursementHandler(disbursementService *disbursement.Service, validator *validation.Validator) *DisbursementHandler {
	return &DisbursementHandler{
		disbursementService: disbursementService,
		validator:           validator,
	}
}

func (h *DisbursementHandler) Create(c *gin.Context) {
	reqIDStr := middleware.RequestIDFromContext(c.Request.Context())
	requestID, err := uuid.Parse(reqIDStr)
	if err != nil {
		requestID = uuid.New()
	}

	identity, ok := middleware.UserIdentityFromContext(c.Request.Context())
	if !ok {
		response.WriteError(c.Writer, reqIDStr, domain.Unauthorized())
		return
	}

	var req dto.CreateDisbursementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.Validation([]domain.FieldError{
			{Field: "body", Message: "format JSON tidak valid"},
		}))
		return
	}

	if details := h.validator.Validate(req); len(details) > 0 {
		response.WriteError(c.Writer, reqIDStr, domain.Validation(details))
		return
	}

	idempotencyKey := c.GetHeader("Idempotency-Key")

	input := domain.CreateDisbursementInput{
		RecipientName: req.RecipientName,
		AccountNumber: req.AccountNumber,
		BankCode:      req.BankCode,
		Amount:        req.Amount,
		Note:          req.Note,
	}

	result, err := h.disbursementService.Create(c.Request.Context(), identity.ID, requestID, idempotencyKey, input)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, err)
		return
	}

	if result.IsReplay && result.ReplayResponse != nil {
		c.Header("X-Idempotent-Replayed", "true")
		response.WriteSuccess(c.Writer, result.ReplayResponse.StatusCode, json.RawMessage(result.ReplayResponse.Body), nil)
		return
	}

	response.WriteSuccess(c.Writer, http.StatusCreated, dto.NewDisbursementResponse(result.Disbursement), nil)
}

func (h *DisbursementHandler) GetByID(c *gin.Context) {
	reqIDStr := middleware.RequestIDFromContext(c.Request.Context())

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.DisbursementNotFound())
		return
	}

	d, err := h.disbursementService.GetByID(c.Request.Context(), id)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, err)
		return
	}

	response.WriteSuccess(c.Writer, http.StatusOK, dto.NewDisbursementResponse(d), nil)
}

func (h *DisbursementHandler) List(c *gin.Context) {
	reqIDStr := middleware.RequestIDFromContext(c.Request.Context())

	var query dto.ListDisbursementsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.Validation([]domain.FieldError{
			{Field: "query", Message: "parameter query tidak valid"},
		}))
		return
	}

	if details := h.validator.Validate(query); len(details) > 0 {
		response.WriteError(c.Writer, reqIDStr, domain.Validation(details))
		return
	}

	var dateRange *domain.DateRange
	if query.DateFrom != "" || query.DateTo != "" {
		fromStr := query.DateFrom
		if fromStr == "" {
			fromStr = query.DateTo
		}
		toStr := query.DateTo
		if toStr == "" {
			toStr = query.DateFrom
		}

		fromDate, errFrom := time.Parse("2006-01-02", fromStr)
		toDate, errTo := time.Parse("2006-01-02", toStr)

		if errFrom != nil || errTo != nil {
			response.WriteError(c.Writer, reqIDStr, domain.Validation([]domain.FieldError{
				{Field: "date_range", Message: "format tanggal harus YYYY-MM-DD"},
			}))
			return
		}

		dr, err := domain.NewUTCDateRange(fromDate, toDate)
		if err != nil {
			response.WriteError(c.Writer, reqIDStr, err)
			return
		}
		dateRange = &dr
	}

	filter := repository.DisbursementFilter{
		Page:      query.Page,
		Limit:     query.Limit,
		Status:    domain.DisbursementStatus(strings.ToUpper(query.Status)),
		Search:    query.Search,
		SortBy:    query.SortBy,
		SortOrder: query.SortOrder,
		DateRange: dateRange,
	}

	items, pagination, err := h.disbursementService.List(c.Request.Context(), filter)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, err)
		return
	}

	data := make([]dto.DisbursementResponse, len(items))
	for i, item := range items {
		data[i] = dto.NewDisbursementResponse(item)
	}

	meta := &response.Meta{
		Page:       pagination.Page,
		Limit:      pagination.Limit,
		Total:      pagination.Total,
		TotalPages: pagination.TotalPages,
	}

	response.WriteSuccess(c.Writer, http.StatusOK, data, meta)
}

func (h *DisbursementHandler) UpdateStatus(c *gin.Context) {
	reqIDStr := middleware.RequestIDFromContext(c.Request.Context())
	requestID, err := uuid.Parse(reqIDStr)
	if err != nil {
		requestID = uuid.New()
	}

	identity, ok := middleware.UserIdentityFromContext(c.Request.Context())
	if !ok {
		response.WriteError(c.Writer, reqIDStr, domain.Unauthorized())
		return
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.DisbursementNotFound())
		return
	}

	var req dto.UpdateDisbursementStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.Validation([]domain.FieldError{
			{Field: "body", Message: "format JSON tidak valid"},
		}))
		return
	}

	if details := h.validator.Validate(req); len(details) > 0 {
		response.WriteError(c.Writer, reqIDStr, domain.Validation(details))
		return
	}

	decision := domain.Decision{
		Status:  domain.DisbursementStatus(strings.ToUpper(req.Status)),
		ActorID: identity.ID,
		Note:    req.Note,
	}

	updated, err := h.disbursementService.UpdateStatus(c.Request.Context(), identity.ID, requestID, id, decision)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, err)
		return
	}

	response.WriteSuccess(c.Writer, http.StatusOK, dto.NewDisbursementResponse(updated), nil)
}

func (h *DisbursementHandler) Delete(c *gin.Context) {
	reqIDStr := middleware.RequestIDFromContext(c.Request.Context())
	requestID, err := uuid.Parse(reqIDStr)
	if err != nil {
		requestID = uuid.New()
	}

	identity, ok := middleware.UserIdentityFromContext(c.Request.Context())
	if !ok {
		response.WriteError(c.Writer, reqIDStr, domain.Unauthorized())
		return
	}

	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, domain.DisbursementNotFound())
		return
	}

	_, _, err = h.disbursementService.SoftDelete(c.Request.Context(), identity.ID, requestID, id)
	if err != nil {
		response.WriteError(c.Writer, reqIDStr, err)
		return
	}

	response.WriteNoContent(c.Writer)
}
