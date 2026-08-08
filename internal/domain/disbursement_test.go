package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCalculateAdminFeeBoundaryAmounts(t *testing.T) {
	tests := []struct {
		name      string
		amount    int64
		wantFee   int64
		wantError bool
	}{
		{name: "below minimum", amount: 9_999, wantError: true},
		{name: "minimum", amount: 10_000, wantFee: LowerTierAdminFee},
		{name: "below fee threshold", amount: 4_999_999, wantFee: LowerTierAdminFee},
		{name: "fee threshold", amount: 5_000_000, wantFee: UpperTierAdminFee},
		{name: "maximum", amount: 100_000_000_000, wantFee: UpperTierAdminFee},
		{name: "above maximum", amount: 100_000_000_001, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fee, err := CalculateAdminFee(test.amount)
			if test.wantError {
				if err == nil {
					t.Fatalf("CalculateAdminFee(%d) error = nil, want error", test.amount)
				}
				return
			}
			if err != nil {
				t.Fatalf("CalculateAdminFee(%d) error = %v", test.amount, err)
			}
			if fee != test.wantFee {
				t.Errorf("CalculateAdminFee(%d) = %d, want %d", test.amount, fee, test.wantFee)
			}
		})
	}
}

func TestCreateDisbursementInputValidation(t *testing.T) {
	base := CreateDisbursementInput{
		RecipientName: "Budi Santoso",
		AccountNumber: "123456",
		BankCode:      "bca",
		Amount:        10_000,
	}

	tests := []struct {
		name      string
		input     CreateDisbursementInput
		wantField string
		wantValid bool
	}{
		{name: "valid input", input: base, wantValid: true},
		{name: "recipient at 150 runes", input: CreateDisbursementInput{RecipientName: strings.Repeat("界", 150), AccountNumber: base.AccountNumber, BankCode: base.BankCode, Amount: base.Amount}, wantValid: true},
		{name: "recipient at 151 runes", input: CreateDisbursementInput{RecipientName: strings.Repeat("界", 151), AccountNumber: base.AccountNumber, BankCode: base.BankCode, Amount: base.Amount}, wantField: "recipient_name"},
		{name: "account with five digits", input: CreateDisbursementInput{RecipientName: base.RecipientName, AccountNumber: "12345", BankCode: base.BankCode, Amount: base.Amount}, wantField: "account_number"},
		{name: "account with six digits", input: CreateDisbursementInput{RecipientName: base.RecipientName, AccountNumber: "123456", BankCode: base.BankCode, Amount: base.Amount}, wantValid: true},
		{name: "account with non-digit", input: CreateDisbursementInput{RecipientName: base.RecipientName, AccountNumber: "12345A", BankCode: base.BankCode, Amount: base.Amount}, wantField: "account_number"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.input.Validate()
			if test.wantValid {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
			domainErr := AsError(err)
			if domainErr.Code != CodeValidationError {
				t.Errorf("Validate() code = %s, want %s", domainErr.Code, CodeValidationError)
			}
			if len(domainErr.Details) != 1 || domainErr.Details[0].Field != test.wantField {
				t.Errorf("Validate() details = %#v, want field %q", domainErr.Details, test.wantField)
			}
		})
	}
}

func TestCanonicalBankCodeUppercasesBCA(t *testing.T) {
	canonical, err := CanonicalBankCode("bca")
	if err != nil {
		t.Fatalf("CanonicalBankCode() error = %v", err)
	}
	if canonical != "BCA" {
		t.Errorf("CanonicalBankCode(\"bca\") = %q, want %q", canonical, "BCA")
	}
}

func TestDisbursementStatusTransitions(t *testing.T) {
	tests := []struct {
		name string
		from DisbursementStatus
		to   DisbursementStatus
		want bool
	}{
		{name: "pending to approved", from: StatusPending, to: StatusApproved, want: true},
		{name: "pending to rejected", from: StatusPending, to: StatusRejected, want: true},
		{name: "approved to rejected", from: StatusApproved, to: StatusRejected},
		{name: "rejected to approved", from: StatusRejected, to: StatusApproved},
		{name: "pending to pending", from: StatusPending, to: StatusPending},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.from.CanTransitionTo(test.to); got != test.want {
				t.Errorf("CanTransitionTo(%q, %q) = %t, want %t", test.from, test.to, got, test.want)
			}
		})
	}
}

func TestCanDeleteOnlyPendingUndeletedDisbursement(t *testing.T) {
	deletedAt := time.Date(2026, time.August, 8, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    DisbursementStatus
		deletedAt *time.Time
		want      bool
	}{
		{name: "pending and not deleted", status: StatusPending, want: true},
		{name: "pending and already deleted", status: StatusPending, deletedAt: &deletedAt},
		{name: "approved and not deleted", status: StatusApproved},
		{name: "rejected and not deleted", status: StatusRejected},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanDelete(test.status, test.deletedAt); got != test.want {
				t.Errorf("CanDelete(%q, %v) = %t, want %t", test.status, test.deletedAt, got, test.want)
			}
		})
	}
}

func TestNewUTCDateRangeUsesHalfOpenEqualUTCDate(t *testing.T) {
	wib := time.FixedZone("WIB", 7*60*60)
	from := time.Date(2026, time.August, 8, 23, 59, 0, 0, wib)
	to := time.Date(2026, time.August, 9, 0, 1, 0, 0, wib)

	dateRange, err := NewUTCDateRange(from, to)
	if err != nil {
		t.Fatalf("NewUTCDateRange() error = %v", err)
	}
	wantFrom := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	if !dateRange.FromInclusive.Equal(wantFrom) {
		t.Errorf("FromInclusive = %s, want %s", dateRange.FromInclusive, wantFrom)
	}
	if !dateRange.ToExclusive.Equal(wantTo) {
		t.Errorf("ToExclusive = %s, want %s", dateRange.ToExclusive, wantTo)
	}
}

func TestNewUTCDateRangeRejectsInvertedDates(t *testing.T) {
	from := time.Date(2026, time.August, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.August, 8, 23, 59, 0, 0, time.UTC)

	_, err := NewUTCDateRange(from, to)
	if err == nil {
		t.Fatal("NewUTCDateRange() error = nil, want validation error")
	}
	if got := AsError(err).Code; got != CodeValidationError {
		t.Errorf("NewUTCDateRange() code = %s, want %s", got, CodeValidationError)
	}
}

func TestNewPaginationDefaultsRoundsAndRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		page      int
		limit     int
		total     int
		want      Pagination
		wantError bool
	}{
		{name: "defaults", total: 0, want: Pagination{Page: 1, Limit: 20, Total: 0, TotalPages: 0}},
		{name: "rounds up", page: 2, limit: 25, total: 51, want: Pagination{Page: 2, Limit: 25, Total: 51, TotalPages: 3}},
		{name: "exact pages", page: 1, limit: 20, total: 40, want: Pagination{Page: 1, Limit: 20, Total: 40, TotalPages: 2}},
		{name: "negative page", page: -1, limit: 20, total: 1, wantError: true},
		{name: "negative limit", page: 1, limit: -1, total: 1, wantError: true},
		{name: "limit too large", page: 1, limit: 101, total: 1, wantError: true},
		{name: "negative total", page: 1, limit: 20, total: -1, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pagination, err := NewPagination(test.page, test.limit, test.total)
			if test.wantError {
				if err == nil {
					t.Fatal("NewPagination() error = nil, want validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPagination() error = %v", err)
			}
			if pagination != test.want {
				t.Errorf("NewPagination() = %#v, want %#v", pagination, test.want)
			}
		})
	}
}

func TestDecisionValidation(t *testing.T) {
	actorID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440001")
	tests := []struct {
		name      string
		decision  Decision
		wantValid bool
	}{
		{name: "approved decision", decision: Decision{Status: StatusApproved, ActorID: actorID, Note: "approved"}, wantValid: true},
		{name: "rejected decision", decision: Decision{Status: StatusRejected, ActorID: actorID}, wantValid: true},
		{name: "nil actor", decision: Decision{Status: StatusApproved}},
		{name: "invalid status", decision: Decision{Status: StatusPending, ActorID: actorID}},
		{name: "note too long", decision: Decision{Status: StatusApproved, ActorID: actorID, Note: strings.Repeat("x", MaximumNoteRunes+1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.decision.Validate()
			if test.wantValid {
				if err != nil {
					t.Fatalf("Decision.Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Decision.Validate() error = nil, want validation error")
			}
			if got := AsError(err).Code; got != CodeValidationError {
				t.Errorf("Decision.Validate() code = %s, want %s", got, CodeValidationError)
			}
		})
	}
}
