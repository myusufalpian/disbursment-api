package httpapi

import (
	"net/http"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/httpapi/middleware"
	"disbursment-api/internal/httpapi/response"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/service/auth"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authService *auth.Service
	validator   *validation.Validator
}

func NewAuthHandler(authService *auth.Service, validator *validation.Validator) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		validator:   validator,
	}
}

func (handler *AuthHandler) Login(context *gin.Context) {
	requestID := middleware.RequestIDFromContext(context.Request.Context())
	var request dto.LoginRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		response.WriteError(context.Writer, requestID, domain.Validation([]domain.FieldError{
			{Field: "body", Message: "format JSON tidak valid"},
		}))
		return
	}

	if details := handler.validator.Validate(request); len(details) > 0 {
		response.WriteError(context.Writer, requestID, domain.Validation(details))
		return
	}

	tokenResponse, err := handler.authService.Login(context.Request.Context(), request)
	if err != nil {
		response.WriteError(context.Writer, requestID, err)
		return
	}

	response.WriteSuccess(context.Writer, http.StatusOK, tokenResponse, nil)
}

func (handler *AuthHandler) Refresh(context *gin.Context) {
	requestID := middleware.RequestIDFromContext(context.Request.Context())
	var request dto.RefreshRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		response.WriteError(context.Writer, requestID, domain.Validation([]domain.FieldError{
			{Field: "body", Message: "format JSON tidak valid"},
		}))
		return
	}

	if details := handler.validator.Validate(request); len(details) > 0 {
		response.WriteError(context.Writer, requestID, domain.Validation(details))
		return
	}

	tokenResponse, err := handler.authService.Refresh(context.Request.Context(), request)
	if err != nil {
		response.WriteError(context.Writer, requestID, err)
		return
	}

	response.WriteSuccess(context.Writer, http.StatusOK, tokenResponse, nil)
}

func (handler *AuthHandler) Logout(context *gin.Context) {
	requestID := middleware.RequestIDFromContext(context.Request.Context())
	var request dto.LogoutRequest
	if err := context.ShouldBindJSON(&request); err != nil {
		response.WriteError(context.Writer, requestID, domain.Validation([]domain.FieldError{
			{Field: "body", Message: "format JSON tidak valid"},
		}))
		return
	}

	if details := handler.validator.Validate(request); len(details) > 0 {
		response.WriteError(context.Writer, requestID, domain.Validation(details))
		return
	}

	err := handler.authService.Logout(context.Request.Context(), request)
	if err != nil {
		response.WriteError(context.Writer, requestID, err)
		return
	}

	response.WriteNoContent(context.Writer)
}
