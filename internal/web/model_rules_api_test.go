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

func TestServer_API_ModelRulesAndCategorySelect(t *testing.T) {
	_, accRepo, quotaRepo, _, metricsSvc, broadcaster, eventRepo := setupTestWeb(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, id := range []string{"acc-1", "acc-2"} {
		if err := accRepo.Create(ctx, &domain.Account{
			ID: id, Email: id + "@x.com", AccessToken: "t", IsActive: i == 0,
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

	// Per-category select must not move the global active account.
	if r := do(http.MethodPost, "/api/accounts/acc-2/select", `{"category":"gemini_argon"}`); r.StatusCode != 200 {
		t.Fatalf("category select: %d", r.StatusCode)
	}
	if a, _ := accRepo.GetActive(ctx); a.ID != "acc-1" {
		t.Fatalf("global active changed to %s", a.ID)
	}
	if r := do(http.MethodPost, "/api/accounts/acc-2/select", `{"category":"bogus"}`); r.StatusCode != 400 {
		t.Fatalf("expected 400, got %d", r.StatusCode)
	}
	if r := do(http.MethodPost, "/api/accounts/acc-2/select", `{"category":`); r.StatusCode != 400 {
		t.Fatalf("expected 400 on malformed json, got %d", r.StatusCode)
	}

	// Rule update.
	if r := do(http.MethodPut, "/api/accounts/acc-1/models", `{"model_id":"gemini-4-argon-high","enabled":false}`); r.StatusCode != 200 {
		t.Fatalf("put rule: %d", r.StatusCode)
	}
	if r := do(http.MethodPut, "/api/accounts/ghost/models", `{"model_id":"m"}`); r.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", r.StatusCode)
	}

	var list []*AccountWithBuckets
	if err := json.NewDecoder(do(http.MethodGet, "/api/accounts", "").Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		switch a.ID {
		case "acc-1":
			if len(a.ModelRules) != 1 || a.ModelRules[0].Enabled || a.ModelRules[0].Category != "gemini_argon" {
				t.Fatalf("unexpected rules: %+v", a.ModelRules)
			}
		case "acc-2":
			found := false
			for _, c := range a.ActiveCategories {
				found = found || c == "gemini_argon"
			}
			if !found {
				t.Fatalf("acc-2 active_categories missing gemini_argon: %v", a.ActiveCategories)
			}
		}
	}
}
