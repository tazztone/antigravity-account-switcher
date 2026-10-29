package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestServer_API_CategoryRoutingAndExclusion(t *testing.T) {
	_, accRepo, quotaRepo, _, metricsSvc, broadcaster, eventRepo := setupTestWeb(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, id := range []string{"acc-1", "acc-2"} {
		if err := accRepo.Create(ctx, &domain.Account{
			ID: id, Email: id + "@example.com", AccessToken: "t", IsActive: i == 0,
			Status: domain.AccountStatusActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server, err := NewServer(accRepo, quotaRepo, metricsSvc, broadcaster, eventRepo, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server)
	defer ts.Close()

	do := func(method, path, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBufferString(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// 1. Select with mode: set claude_gpt to solo on acc-1
	resp := do(http.MethodPost, "/api/accounts/acc-1/select", `{"category":"claude_gpt","mode":"solo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var selectRes map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&selectRes); err != nil {
		t.Fatal(err)
	}
	if selectRes["mode"] != "solo" {
		t.Fatalf("expected mode solo, got %v", selectRes["mode"])
	}

	mode, err := accRepo.GetCategoryRoutingMode(ctx, domain.CategoryClaudeGPT)
	if err != nil || mode != domain.RoutingModeSolo {
		t.Fatalf("expected solo mode in repo, got mode=%s, err=%v", mode, err)
	}

	// 2. GET /api/status includes category_modes
	statusResp := do(http.MethodGet, "/api/status", "")
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for status, got %d", statusResp.StatusCode)
	}
	var statusData StatusResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&statusData); err != nil {
		t.Fatal(err)
	}
	if statusData.CategoryModes["claude_gpt"] != "solo" {
		t.Fatalf("expected claude_gpt to be solo in status response, got %v", statusData.CategoryModes)
	}

	// 3. PUT /api/accounts/acc-2/categories disables claude_gpt
	putResp := do(http.MethodPut, "/api/accounts/acc-2/categories", `{"category":"claude_gpt","enabled":false}`)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for PUT categories, got %d", putResp.StatusCode)
	}

	// Verify account in repo has disabled category
	acc2, err := accRepo.GetByID(ctx, "acc-2")
	if err != nil {
		t.Fatal(err)
	}
	if !acc2.IsCategoryDisabled(domain.CategoryClaudeGPT) {
		t.Fatalf("expected acc-2 to have claude_gpt disabled")
	}

	// 4. GET /api/accounts reflects category_modes and disabled_categories
	accountsResp := do(http.MethodGet, "/api/accounts", "")
	if accountsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for accounts list, got %d", accountsResp.StatusCode)
	}
	var accountsList []*AccountWithBuckets
	if err := json.NewDecoder(accountsResp.Body).Decode(&accountsList); err != nil {
		t.Fatal(err)
	}
	if len(accountsList) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accountsList))
	}
	for _, a := range accountsList {
		if a.CategoryModes["claude_gpt"] != "solo" {
			t.Fatalf("expected claude_gpt to be solo on account %s, got %v", a.ID, a.CategoryModes)
		}
		if a.ID == "acc-2" {
			if !a.IsCategoryDisabled(domain.CategoryClaudeGPT) {
				t.Fatalf("expected acc-2 to have claude_gpt in disabled_categories, got %v", a.DisabledCategories)
			}
		}
	}

	// 5. Validation errors
	if r := do(http.MethodPut, "/api/accounts/acc-2/categories", `{"category":"invalid_cat","enabled":false}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid category, got %d", r.StatusCode)
	}
	if r := do(http.MethodPut, "/api/accounts/nonexistent/categories", `{"category":"claude_gpt","enabled":false}`); r.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent account, got %d", r.StatusCode)
	}
}
