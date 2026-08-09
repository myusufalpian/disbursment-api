package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"disbursment-api/internal/domain"
	"disbursment-api/internal/httpapi"
	"disbursment-api/internal/httpapi/dto"
	"disbursment-api/internal/httpapi/validation"
	"disbursment-api/internal/repository"
	"disbursment-api/internal/service/disbursement"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type mockDisbursementStore struct {
	items map[uuid.UUID]domain.Disbursement
}

func newMockDisbursementStore() *mockDisbursementStore {
	return &mockDisbursementStore{
		items: make(map[uuid.UUID]domain.Disbursement),
	}
}

func (m *mockDisbursementStore) Insert(ctx context.Context, tx repository.Transaction, d domain.Disbursement) error {
	m.items[d.ID] = d
	return nil
}

func (m *mockDisbursementStore) FindByID(ctx context.Context, id uuid.UUID) (domain.Disbursement, error) {
	d, ok := m.items[id]
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	return d, nil
}

func (m *mockDisbursementStore) List(ctx context.Context, filter repository.DisbursementFilter) ([]domain.Disbursement, int, error) {
	var result []domain.Disbursement
	for _, d := range m.items {
		if d.DeletedAt != nil {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		result = append(result, d)
	}
	return result, len(result), nil
}

func (m *mockDisbursementStore) UpdateStatus(ctx context.Context, tx repository.Transaction, id uuid.UUID, decision domain.Decision) (domain.Disbursement, error) {
	d, ok := m.items[id]
	if !ok || d.DeletedAt != nil {
		return domain.Disbursement{}, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, repository.NewError(repository.ErrorConflict, nil)
	}
	d.Status = decision.Status
	d.DecidedBy = decision.ActorID
	d.DecisionNote = decision.Note
	m.items[id] = d
	return d, nil
}

func (m *mockDisbursementStore) SoftDelete(ctx context.Context, tx repository.Transaction, id uuid.UUID) (domain.Disbursement, bool, error) {
	d, ok := m.items[id]
	if !ok {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorNotFound, nil)
	}
	if d.DeletedAt != nil {
		return d, true, nil
	}
	if d.Status != domain.StatusPending {
		return domain.Disbursement{}, false, repository.NewError(repository.ErrorConstraint, nil)
	}
	now := d.CreatedAt
	d.DeletedAt = &now
	m.items[id] = d
	return d, false, nil
}

type mockAuditOutboxStore struct {
	events []domain.AuditEvent
}

func (m *mockAuditOutboxStore) Insert(ctx context.Context, tx repository.Transaction, event domain.AuditEvent) error {
	m.events = append(m.events, event)
	return nil
}
func (m *mockAuditOutboxStore) FetchPending(ctx context.Context, limit int) ([]domain.AuditEvent, error) {
	return nil, nil
}
func (m *mockAuditOutboxStore) MarkDelivered(ctx context.Context, eventID uuid.UUID, deliveredAt time.Time) error {
	return nil
}
func (m *mockAuditOutboxStore) RecordFailure(ctx context.Context, eventID uuid.UUID, errMessage string, nextAvailableAt time.Time) error {
	return nil
}
func (m *mockAuditOutboxStore) ReconcilePending(ctx context.Context, minAge time.Duration) (int, int, error) {
	return 0, 0, nil
}
func (m *mockAuditOutboxStore) CleanupDelivered(ctx context.Context, olderThan time.Duration) (int64, error) {
	return 0, nil
}

type mockTransactor struct{}
type mockTransaction struct{}

func (m mockTransaction) Context() context.Context { return context.Background() }
func (m mockTransactor) WithinTransaction(ctx context.Context, fn func(context.Context, repository.Transaction) error) error {
	return fn(ctx, mockTransaction{})
}

func generateTestToken(secret string, userID uuid.UUID, role domain.UserRole) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      userID.String(),
		"username": "test_user",
		"role":     string(role),
		"iss":      "disbursement-api",
		"aud":      "disbursement-api-users",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	tokenStr, _ := token.SignedString([]byte(secret))
	return tokenStr
}

func TestDisbursementEndpointsIntegration(t *testing.T) {
	jwtSecret := "test-secret-key-12345"
	operatorID := uuid.New()
	adminID := uuid.New()
	superadminID := uuid.New()

	operatorToken := generateTestToken(jwtSecret, operatorID, domain.RoleOperator)
	adminToken := generateTestToken(jwtSecret, adminID, domain.RoleAdmin)
	superadminToken := generateTestToken(jwtSecret, superadminID, domain.RoleSuperadmin)

	store := newMockDisbursementStore()
	outboxStore := &mockAuditOutboxStore{}
	transactor := mockTransactor{}

	disbursementService, err := disbursement.NewService(store, outboxStore, transactor, nil, nil)
	if err != nil {
		t.Fatalf("failed to create disbursement service: %v", err)
	}

	validatorEngine, err := validation.New()
	if err != nil {
		t.Fatalf("validator init failed: %v", err)
	}

	handler := httpapi.NewDisbursementHandler(disbursementService, validatorEngine)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := httpapi.NewRouter(1<<20, logger, jwtSecret, "disbursement-api", "disbursement-api-users", nil, handler, nil, "test-metrics-token", nil)
	if err != nil {
		t.Fatalf("router init failed: %v", err)
	}

	var createdID string

	t.Run("POST /disbursements - Create Disbursement", func(t *testing.T) {
		body, _ := json.Marshal(dto.CreateDisbursementRequest{
			RecipientName: "Jane Doe",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        500000,
			Note:          "Test payment",
		})

		req, _ := http.NewRequest(http.MethodPost, "/disbursements", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)

		data, ok := resp["data"].(map[string]any)
		if !ok {
			t.Fatalf("expected data object in response")
		}

		createdID = data["id"].(string)
		if data["status"] != "PENDING" {
			t.Errorf("expected status PENDING, got %v", data["status"])
		}
		if int64(data["admin_fee"].(float64)) != domain.LowerTierAdminFee {
			t.Errorf("expected admin fee %d, got %v", domain.LowerTierAdminFee, data["admin_fee"])
		}
	})

	t.Run("POST /disbursements - Invalid JSON body", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "/disbursements", bytes.NewReader([]byte("{invalid-json")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for malformed JSON, got %d", w.Code)
		}
	})

	t.Run("POST /disbursements - Validation error", func(t *testing.T) {
		body, _ := json.Marshal(dto.CreateDisbursementRequest{
			RecipientName: "",
			AccountNumber: "123", // too short
			BankCode:      "BCA",
			Amount:        5000, // too low
		})
		req, _ := http.NewRequest(http.MethodPost, "/disbursements", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for validation error, got %d", w.Code)
		}
	})

	t.Run("GET /disbursements - List Disbursements", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/disbursements?page=1&limit=10", nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("GET /disbursements - Invalid Date Filter", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/disbursements?date_from=invalid-date", nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for invalid date_from, got %d", w.Code)
		}
	})

	t.Run("GET /disbursements/:id - Get Detail", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/disbursements/"+createdID, nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("GET /disbursements/:id - Invalid UUID path param", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/disbursements/not-a-uuid", nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for invalid UUID, got %d", w.Code)
		}
	})

	t.Run("GET /disbursements/:id - Non-existent ID returns 404", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/disbursements/"+uuid.New().String(), nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found, got %d", w.Code)
		}
	})

	t.Run("PATCH /disbursements/:id/status - Operator Forbidden (403)", func(t *testing.T) {
		body, _ := json.Marshal(dto.UpdateDisbursementStatusRequest{
			Status: "APPROVED",
			Note:   "Operator approval attempt",
		})
		req, _ := http.NewRequest(http.MethodPatch, "/disbursements/"+createdID+"/status", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Fatalf("expected status 403 Forbidden for operator, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("PATCH /disbursements/:id/status - Admin Approve", func(t *testing.T) {
		body, _ := json.Marshal(dto.UpdateDisbursementStatusRequest{
			Status: "APPROVED",
			Note:   "Approved by Admin",
		})
		req, _ := http.NewRequest(http.MethodPatch, "/disbursements/"+createdID+"/status", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("DELETE /disbursements/:id - Delete Finalized Disbursement Fails", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, "/disbursements/"+createdID, nil)
		req.Header.Set("Authorization", "Bearer "+superadminToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest && w.Code != http.StatusConflict {
			t.Fatalf("expected 400 or 409 when deleting approved disbursement, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("POST /disbursements - Malformed JSON and Validation Failure", func(t *testing.T) {
		// 1. Malformed JSON
		req1, _ := http.NewRequest(http.MethodPost, "/disbursements", bytes.NewReader([]byte(`{invalid`)))
		req1.Header.Set("Content-Type", "application/json")
		req1.Header.Set("Authorization", "Bearer "+operatorToken)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		if w1.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for malformed JSON, got %d", w1.Code)
		}

		// 2. Validation failure (missing RecipientName)
		body, _ := json.Marshal(dto.CreateDisbursementRequest{
			RecipientName: "",
			AccountNumber: "1234567890",
			BankCode:      "BCA",
			Amount:        100000,
		})
		req2, _ := http.NewRequest(http.MethodPost, "/disbursements", bytes.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+operatorToken)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		if w2.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for validation failure, got %d", w2.Code)
		}
	})

	t.Run("GET /disbursements - Valid and Inverted Date Ranges", func(t *testing.T) {
		// 1. Valid date range
		req1, _ := http.NewRequest(http.MethodGet, "/disbursements?date_from=2026-01-01&date_to=2026-01-02", nil)
		req1.Header.Set("Authorization", "Bearer "+operatorToken)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		if w1.Code != http.StatusOK {
			t.Errorf("expected 200 OK for valid date range, got %d", w1.Code)
		}

		// 2. Inverted date range (from > to)
		req2, _ := http.NewRequest(http.MethodGet, "/disbursements?date_from=2026-01-02&date_to=2026-01-01", nil)
		req2.Header.Set("Authorization", "Bearer "+operatorToken)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		if w2.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for inverted date range, got %d", w2.Code)
		}

		// 3. Single date_from specified
		req3, _ := http.NewRequest(http.MethodGet, "/disbursements?date_from=2026-01-01", nil)
		req3.Header.Set("Authorization", "Bearer "+operatorToken)
		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)
		if w3.Code != http.StatusOK {
			t.Errorf("expected 200 OK for date_from, got %d", w3.Code)
		}
	})

	t.Run("PATCH /disbursements/:id/status - Invalid UUID, Malformed JSON, Validation Failure", func(t *testing.T) {
		// 1. Invalid UUID
		req1, _ := http.NewRequest(http.MethodPatch, "/disbursements/invalid-uuid/status", nil)
		req1.Header.Set("Authorization", "Bearer "+adminToken)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		if w1.Code != http.StatusNotFound {
			t.Errorf("expected 404 for invalid UUID, got %d", w1.Code)
		}

		// 2. Malformed JSON
		req2, _ := http.NewRequest(http.MethodPatch, "/disbursements/"+createdID+"/status", bytes.NewReader([]byte(`{invalid`)))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+adminToken)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		if w2.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for malformed JSON, got %d", w2.Code)
		}

		// 3. Validation failure (invalid status)
		body, _ := json.Marshal(dto.UpdateDisbursementStatusRequest{Status: "UNKNOWN_STATUS"})
		req3, _ := http.NewRequest(http.MethodPatch, "/disbursements/"+createdID+"/status", bytes.NewReader(body))
		req3.Header.Set("Content-Type", "application/json")
		req3.Header.Set("Authorization", "Bearer "+adminToken)
		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)
		if w3.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid status, got %d", w3.Code)
		}
	})

	t.Run("DELETE /disbursements/:id - Invalid UUID path", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, "/disbursements/not-a-uuid", nil)
		req.Header.Set("Authorization", "Bearer "+superadminToken)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 Not Found, got %d", w.Code)
		}
	})
}
