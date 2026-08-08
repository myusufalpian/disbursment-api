package dto

import (
	"time"

	"disbursment-api/internal/domain"

	"github.com/google/uuid"
)

type CreateDisbursementRequest struct {
	RecipientName string `json:"recipient_name" validate:"required,maxchars=150"`
	AccountNumber string `json:"account_number" validate:"required,numeric,min=6,max=34"`
	BankCode      string `json:"bank_code" validate:"required,alphanum,min=3,max=10"`
	Amount        int64  `json:"amount" validate:"gte=10000,lte=100000000000"`
	Note          string `json:"note" validate:"omitempty,maxchars=500"`
}

type UpdateDisbursementStatusRequest struct {
	Status string `json:"status" validate:"required,oneof=APPROVED REJECTED"`
	Note   string `json:"note" validate:"omitempty,maxchars=500"`
}

type ListDisbursementsQuery struct {
	Page      int    `form:"page" validate:"omitempty,gte=1"`
	Limit     int    `form:"limit" validate:"omitempty,gte=1,lte=100"`
	Status    string `form:"status" validate:"omitempty,oneof=PENDING APPROVED REJECTED"`
	Search    string `form:"search" validate:"omitempty,maxchars=150"`
	SortBy    string `form:"sort_by" validate:"omitempty,oneof=created_at amount recipient_name status"`
	SortOrder string `form:"sort_order" validate:"omitempty,oneof=asc desc"`
	DateFrom  string `form:"date_from" validate:"omitempty,datetime=2006-01-02"`
	DateTo    string `form:"date_to" validate:"omitempty,datetime=2006-01-02"`
}

type DisbursementResponse struct {
	ID            uuid.UUID                 `json:"id"`
	RecipientName string                    `json:"recipient_name"`
	AccountNumber string                    `json:"account_number"`
	BankCode      string                    `json:"bank_code"`
	Amount        int64                     `json:"amount"`
	AdminFee      int64                     `json:"admin_fee"`
	Status        domain.DisbursementStatus `json:"status"`
	Note          string                    `json:"note"`
	CreatedBy     uuid.UUID                 `json:"created_by"`
	DecidedBy     *uuid.UUID                `json:"decided_by"`
	ApprovedBy    *uuid.UUID                `json:"approved_by"`
	DecisionNote  string                    `json:"decision_note"`
	DecidedAt     *time.Time                `json:"decided_at"`
	CreatedAt     time.Time                 `json:"created_at"`
	UpdatedAt     time.Time                 `json:"updated_at"`
}

func NewDisbursementResponse(d domain.Disbursement) DisbursementResponse {
	var decidedBy *uuid.UUID
	if d.DecidedBy != uuid.Nil {
		decidedBy = &d.DecidedBy
	}
	return DisbursementResponse{
		ID:            d.ID,
		RecipientName: d.RecipientName,
		AccountNumber: d.AccountNumber,
		BankCode:      d.BankCode,
		Amount:        d.Amount,
		AdminFee:      d.AdminFee,
		Status:        d.Status,
		Note:          d.Note,
		CreatedBy:     d.CreatedBy,
		DecidedBy:     decidedBy,
		ApprovedBy:    decidedBy,
		DecisionNote:  d.DecisionNote,
		DecidedAt:     d.DecidedAt,
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
	}
}
