package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
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
	search := strings.ToLower(strings.TrimSpace(filter.Search))
	result := make([]domain.Disbursement, 0, len(m.items))
	for _, d := range m.items {
		if d.DeletedAt != nil {
			continue
		}
		if filter.Status != "" && d.Status != filter.Status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(d.RecipientName), search) && !strings.Contains(strings.ToLower(d.AccountNumber), search) {
			continue
		}
		if filter.DateRange != nil && (d.CreatedAt.Before(filter.DateRange.FromInclusive) || !d.CreatedAt.Before(filter.DateRange.ToExclusive)) {
			continue
		}
		result = append(result, d)
	}

	sort.SliceStable(result, func(i, j int) bool {
		left := result[i]
		right := result[j]
		comparison := 0
		switch filter.SortBy {
		case "amount":
			switch {
			case left.Amount < right.Amount:
				comparison = -1
			case left.Amount > right.Amount:
				comparison = 1
			}
		case "recipient_name":
			comparison = strings.Compare(left.RecipientName, right.RecipientName)
		case "status":
			comparison = strings.Compare(string(left.Status), string(right.Status))
		default:
			switch {
			case left.CreatedAt.Before(right.CreatedAt):
				comparison = -1
			case left.CreatedAt.After(right.CreatedAt):
				comparison = 1
			}
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		if strings.ToLower(filter.SortOrder) == "asc" {
			return comparison < 0
		}
		return comparison > 0
	})

	total := len(result)
	page := filter.Page
	if page < 1 {
		page = domain.DefaultPage
	}
	limit := filter.Limit
	if limit < 1 || limit > domain.MaximumLimit {
		limit = domain.DefaultLimit
	}
	start := (page - 1) * limit
	if start >= total {
		return []domain.Disbursement{}, total, nil
	}
	end := start + limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
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
		budiID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
		citraID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
		listCreatedBy := operatorID
		store.items[budiID] = domain.Disbursement{
			ID:            budiID,
			RecipientName: "Budi Santoso",
			AccountNumber: "2222222222",
			BankCode:      "BCA",
			Amount:        125000,
			AdminFee:      domain.LowerTierAdminFee,
			Status:        domain.StatusPending,
			CreatedBy:     listCreatedBy,
			CreatedAt:     time.Date(2026, time.January, 2, 10, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, time.January, 2, 10, 0, 0, 0, time.UTC),
		}
		store.items[citraID] = domain.Disbursement{
			ID:            citraID,
			RecipientName: "Citra Sari",
			AccountNumber: "3333333333",
			BankCode:      "BRI",
			Amount:        75000,
			AdminFee:      domain.LowerTierAdminFee,
			Status:        domain.StatusPending,
			CreatedBy:     listCreatedBy,
			CreatedAt:     time.Date(2026, time.January, 3, 10, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, time.January, 3, 10, 0, 0, 0, time.UTC),
		}

		req := httptest.NewRequest(http.MethodGet, "/disbursements?page=1&limit=10&status=PENDING", nil)
		req.Header.Set("Authorization", "Bearer "+operatorToken)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", w.Code, w.Body.String())
		}

		var listResponse struct {
			Success bool                       `json:"success"`
			Data    []dto.DisbursementResponse `json:"data"`
			Meta    *struct {
				Page       int `json:"page"`
				Limit      int `json:"limit"`
				Total      int `json:"total"`
				TotalPages int `json:"total_pages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listResponse); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		if !listResponse.Success {
			t.Fatal("expected successful list response")
		}
		if len(listResponse.Data) != 3 {
			t.Fatalf("list data length = %d, want 3", len(listResponse.Data))
		}
		if listResponse.Meta == nil || listResponse.Meta.Total != 3 || listResponse.Meta.TotalPages != 1 {
			t.Fatalf("list metadata = %+v, want total=3 total_pages=1", listResponse.Meta)
		}

		searchRequest := httptest.NewRequest(http.MethodGet, "/disbursements?search=budi", nil)
		searchRequest.Header.Set("Authorization", "Bearer "+operatorToken)
		searchResponse := httptest.NewRecorder()
		router.ServeHTTP(searchResponse, searchRequest)
		if searchResponse.Code != http.StatusOK {
			t.Fatalf("search status = %d, want 200: %s", searchResponse.Code, searchResponse.Body.String())
		}
		var searchPayload struct {
			Data []dto.DisbursementResponse `json:"data"`
		}
		if err := json.Unmarshal(searchResponse.Body.Bytes(), &searchPayload); err != nil {
			t.Fatalf("decode search response: %v", err)
		}
		if len(searchPayload.Data) != 1 || searchPayload.Data[0].ID != budiID {
			t.Fatalf("search data = %+v, want only Budi %s", searchPayload.Data, budiID)
		}

		pageRequest := httptest.NewRequest(http.MethodGet, "/disbursements?date_from=2026-01-01&date_to=2026-01-31&sort_by=amount&sort_order=asc&page=1&limit=1", nil)
		pageRequest.Header.Set("Authorization", "Bearer "+operatorToken)
		pageResponse := httptest.NewRecorder()
		router.ServeHTTP(pageResponse, pageRequest)
		if pageResponse.Code != http.StatusOK {
			t.Fatalf("date/sort/page status = %d, want 200: %s", pageResponse.Code, pageResponse.Body.String())
		}
		var pagePayload struct {
			Data []dto.DisbursementResponse `json:"data"`
			Meta *struct {
				Page       int `json:"page"`
				Limit      int `json:"limit"`
				Total      int `json:"total"`
				TotalPages int `json:"total_pages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(pageResponse.Body.Bytes(), &pagePayload); err != nil {
			t.Fatalf("decode date/sort/page response: %v", err)
		}
		if len(pagePayload.Data) != 1 || pagePayload.Data[0].ID != citraID {
			t.Fatalf("date/sort/page data = %+v, want only Citra %s", pagePayload.Data, citraID)
		}
		if pagePayload.Meta == nil || pagePayload.Meta.Page != 1 || pagePayload.Meta.Limit != 1 || pagePayload.Meta.Total != 2 || pagePayload.Meta.TotalPages != 2 {
			t.Fatalf("date/sort/page metadata = %+v, want page=1 limit=1 total=2 total_pages=2", pagePayload.Meta)
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
