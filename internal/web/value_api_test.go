package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestServer_API_PlanValueAndBenchmarks(t *testing.T) {
	_, accRepo, quotaRepo, metricsRepo, metricsSvc, broadcaster, eventRepo := setupTestWeb(t)
	ctx := context.Background()

	acc := &domain.Account{
		ID:           "acc-val-1",
		Email:        "val1@gmail.com",
		RefreshToken: "rt-1",
		PlanTier:     "ai_pro",
	}
	if err := accRepo.Create(ctx, acc); err != nil {
		t.Fatalf("Create account: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	snapRepo := quotaRepo.(domain.QuotaSnapshotRepository)
	_, err := snapRepo.RecordSnapshotsIfChanged(ctx, []*domain.QuotaSnapshot{
		{AccountID: acc.ID, BucketID: "acc-val-1-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 1.0, ResetTime: now.Add(72 * time.Hour), Source: "cloud_code", CapturedAt: now.Add(-30 * time.Minute)},
		{AccountID: acc.ID, BucketID: "acc-val-1-3p-weekly", Category: "claude_gpt", Window: "weekly", RemainingFraction: 0.9, ResetTime: now.Add(72 * time.Hour), Source: "cloud_code", CapturedAt: now.Add(-10 * time.Minute)},
	})
	if err != nil {
		t.Fatalf("RecordSnapshotsIfChanged: %v", err)
	}
	if err := metricsRepo.Record(ctx, &domain.TokenMetric{
		AccountID:    acc.ID,
		Model:        "claude-sonnet-5-5",
		PromptTokens: 1_000_000,
		TotalTokens:  1_000_000,
		Timestamp:    now.Add(-20 * time.Minute),
	}); err != nil {
		t.Fatalf("metricsRepo.Record: %v", err)
	}

	srv, err := NewServer(accRepo, quotaRepo, metricsSvc, broadcaster, eventRepo, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 1. GET /api/analytics/value
	resp, err := http.Get(ts.URL + "/api/analytics/value?period=week")
	if err != nil {
		t.Fatalf("GET /api/analytics/value: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var report domain.PlanValueReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.PoolSummary.RealizedAPIUSD < 2.99 || report.PoolSummary.RealizedAPIUSD > 3.01 {
		t.Fatalf("expected realized ~$3.00, got %v", report.PoolSummary.RealizedAPIUSD)
	}
	if report.PoolSummary.AvgClaudeWeeklyCapUSD == nil || *report.PoolSummary.AvgClaudeWeeklyCapUSD < 29.9 || *report.PoolSummary.AvgClaudeWeeklyCapUSD > 30.1 {
		t.Fatalf("expected implied weekly ~$30.00, got %v", report.PoolSummary.AvgClaudeWeeklyCapUSD)
	}

	// 2. GET /api/analytics/export?format=csv
	csvResp, err := http.Get(ts.URL + "/api/analytics/export?period=week&format=csv")
	if err != nil {
		t.Fatalf("GET /api/analytics/export csv: %v", err)
	}
	defer csvResp.Body.Close()
	if csvResp.StatusCode != http.StatusOK || !strings.Contains(csvResp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("unexpected CSV response status=%d ct=%q", csvResp.StatusCode, csvResp.Header.Get("Content-Type"))
	}

	// 3. PUT /api/accounts/{id}/plan
	planReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/accounts/"+acc.ID+"/plan", bytes.NewBufferString(`{"plan_tier":"ai_ultra","monthly_cost_usd":250}`))
	planReq.Header.Set("Content-Type", "application/json")
	planResp, err := http.DefaultClient.Do(planReq)
	if err != nil {
		t.Fatalf("PUT plan: %v", err)
	}
	_ = planResp.Body.Close()
	if planResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT plan status = %d", planResp.StatusCode)
	}
	updatedAcc, _ := accRepo.GetByID(ctx, acc.ID)
	if updatedAcc.PlanTier != "ai_ultra" || updatedAcc.MonthlyCostUSD != 250 {
		t.Fatalf("expected ai_ultra/$250, got %s/$%v", updatedAcc.PlanTier, updatedAcc.MonthlyCostUSD)
	}

	// 4. PUT /api/analytics/pricing
	priceReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/analytics/pricing", bytes.NewBufferString(`{"model_pattern":"custom-test","display_name":"Custom Test","input_per_million":2,"cached_input_per_million":0.2,"cache_write_per_million":2.5,"output_per_million":10}`))
	priceReq.Header.Set("Content-Type", "application/json")
	priceResp, err := http.DefaultClient.Do(priceReq)
	if err != nil {
		t.Fatalf("PUT pricing: %v", err)
	}
	_ = priceResp.Body.Close()
	if priceResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT pricing status = %d", priceResp.StatusCode)
	}

	// 5. PUT /api/analytics/benchmarks
	benchReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/analytics/benchmarks", bytes.NewBufferString(`{"id":"claude-pro","provider":"Anthropic","plan_name":"Claude Pro","monthly_cost_usd":20,"window_5h_api_usd":12,"weekly_api_usd":60,"notes":"Updated"}`))
	benchReq.Header.Set("Content-Type", "application/json")
	benchResp, err := http.DefaultClient.Do(benchReq)
	if err != nil {
		t.Fatalf("PUT benchmarks: %v", err)
	}
	_ = benchResp.Body.Close()
	if benchResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT benchmarks status = %d", benchResp.StatusCode)
	}
}
