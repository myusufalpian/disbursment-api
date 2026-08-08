package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MinimumAmount         int64 = 10_000
	MaximumAmount         int64 = 100_000_000_000
	FeeThreshold          int64 = 5_000_000
	LowerTierAdminFee     int64 = 2_500
	UpperTierAdminFee     int64 = 5_000
	MaximumRecipientRunes       = 150
	MinimumAccountDigits        = 6
	MaximumAccountDigits        = 34
	MinimumBankCodeLength       = 3
	MaximumBankCodeLength       = 10
	MaximumNoteRunes            = 500
	DefaultPage                 = 1
	DefaultLimit                = 20
	MaximumLimit                = 100

	bankCodeValidationMessage = "harus 3 sampai 10 karakter alfanumerik"
)

type DisbursementStatus string

const (
	StatusPending  DisbursementStatus = "PENDING"
	StatusApproved DisbursementStatus = "APPROVED"
	StatusRejected DisbursementStatus = "REJECTED"
)

type CreateDisbursementInput struct {
	RecipientName string
	AccountNumber string
	BankCode      string
	Amount        int64
	Note          string
}

type Disbursement struct {
	ID            uuid.UUID
	RecipientName string
	AccountNumber string
	BankCode      string
	Amount        int64
	AdminFee      int64
	Status        DisbursementStatus
	Note          string
	CreatedBy     uuid.UUID
	DecidedBy     uuid.UUID
	DecisionNote  string
	DecidedAt     *time.Time
	DeletedAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Decision struct {
	Status  DisbursementStatus
	ActorID uuid.UUID
	Note    string
}

type Pagination struct {
	Page       int
	Limit      int
	Total      int
	TotalPages int
}

type DateRange struct {
	FromInclusive time.Time
	ToExclusive   time.Time
}

func CalculateAdminFee(amount int64) (int64, error) {
	if err := ValidateAmount(amount); err != nil {
		return 0, err
	}
	if amount < FeeThreshold {
		return LowerTierAdminFee, nil
	}
	return UpperTierAdminFee, nil
}

func (input CreateDisbursementInput) Validate() error {
	details := make([]FieldError, 0, 5)
	if utf8.RuneCountInString(input.RecipientName) == 0 {
		details = append(details, FieldError{Field: "recipient_name", Message: "wajib diisi"})
	} else if utf8.RuneCountInString(input.RecipientName) > MaximumRecipientRunes {
		details = append(details, FieldError{Field: "recipient_name", Message: fmt.Sprintf("panjang maksimum %d", MaximumRecipientRunes)})
	}
	if !validAccountNumber(input.AccountNumber) {
		details = append(details, FieldError{Field: "account_number", Message: "harus berisi 6 sampai 34 digit"})
	}
	if _, err := CanonicalBankCode(input.BankCode); err != nil {
		details = append(details, FieldError{Field: "bank_code", Message: bankCodeValidationMessage})
	}
	if err := ValidateAmount(input.Amount); err != nil {
		details = append(details, FieldError{Field: "amount", Message: fmt.Sprintf("harus antara %d dan %d", MinimumAmount, MaximumAmount)})
	}
	if utf8.RuneCountInString(input.Note) > MaximumNoteRunes {
		details = append(details, FieldError{Field: "note", Message: fmt.Sprintf("panjang maksimum %d", MaximumNoteRunes)})
	}
	if len(details) > 0 {
		return Validation(details)
	}
	return nil
}

func ValidateAmount(amount int64) error {
	if amount < MinimumAmount || amount > MaximumAmount {
		return Validation([]FieldError{{Field: "amount", Message: fmt.Sprintf("harus antara %d dan %d", MinimumAmount, MaximumAmount)}})
	}
	return nil
}

func CanonicalBankCode(value string) (string, error) {
	canonical := strings.ToUpper(strings.TrimSpace(value))
	if len(canonical) < MinimumBankCodeLength || len(canonical) > MaximumBankCodeLength {
		return "", Validation([]FieldError{{Field: "bank_code", Message: bankCodeValidationMessage}})
	}
	for _, character := range canonical {
		if !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return "", Validation([]FieldError{{Field: "bank_code", Message: bankCodeValidationMessage}})
		}
	}
	return canonical, nil
}

func (status DisbursementStatus) CanTransitionTo(next DisbursementStatus) bool {
	return status == StatusPending && (next == StatusApproved || next == StatusRejected)
}

func (decision Decision) Validate() error {
	if decision.ActorID == uuid.Nil {
		return Validation([]FieldError{{Field: "actor", Message: "wajib diisi"}})
	}
	if decision.Status != StatusApproved && decision.Status != StatusRejected {
		return Validation([]FieldError{{Field: "status", Message: "harus APPROVED atau REJECTED"}})
	}
	if utf8.RuneCountInString(decision.Note) > MaximumNoteRunes {
		return Validation([]FieldError{{Field: "note", Message: fmt.Sprintf("panjang maksimum %d", MaximumNoteRunes)}})
	}
	return nil
}

func CanDelete(status DisbursementStatus, deletedAt *time.Time) bool {
	return deletedAt == nil && status == StatusPending
}

func NewPagination(page, limit, total int) (Pagination, error) {
	if page == 0 {
		page = DefaultPage
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	if page < 1 || limit < 1 || limit > MaximumLimit || total < 0 {
		return Pagination{}, Validation([]FieldError{{Field: "pagination", Message: "tidak valid"}})
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	return Pagination{Page: page, Limit: limit, Total: total, TotalPages: totalPages}, nil
}

func NewUTCDateRange(from, to time.Time) (DateRange, error) {
	fromUTC := startOfUTCDay(from)
	toUTC := startOfUTCDay(to)
	if fromUTC.After(toUTC) {
		return DateRange{}, Validation([]FieldError{{Field: "date_range", Message: "date_from tidak boleh setelah date_to"}})
	}
	return DateRange{FromInclusive: fromUTC, ToExclusive: toUTC.AddDate(0, 0, 1)}, nil
}

func validAccountNumber(value string) bool {
	if len(value) < MinimumAccountDigits || len(value) > MaximumAccountDigits {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func startOfUTCDay(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
