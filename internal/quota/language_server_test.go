package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaultModelCatalog(t *testing.T) {
	catalog := DefaultModelCatalog()
	if len(catalog) == 0 {
		t.Fatal("expected non-empty model catalog")
	}

	foundGemini := false
	foundClaude := false
	foundGPT := false

	for _, m := range catalog {
		if m.ID == "" {
			t.Errorf("expected non-empty model ID")
		}
		if m.DisplayName == "" {
			t.Errorf("expected non-empty display name for %s", m.ID)
		}
		if m.Category == "gemini" {
			foundGemini = true
		}
		if m.Category == "claude_gpt" {
			if m.ID == "claude-sonnet-4-6" {
				foundClaude = true
			}
			if m.ID == "gpt-oss-120b-medium" {
				foundGPT = true
			}
		}
	}

	if !foundGemini {
		t.Error("expected at least one Gemini model in catalog")
	}
	if !foundClaude {
		t.Error("expected claude-sonnet-4-6 in catalog")
	}
	if !foundGPT {
		t.Error("expected gpt-oss-120b-medium in catalog")
	}
}

func TestCategorizeModelID(t *testing.T) {
	tests := []struct {
		modelID  string
		expected string
	}{
		{"gemini-3.8-flash-high", "gemini"},
		{"gemini-3.1-pro-low", "gemini"},
		{"claude-sonnet-4-6", "claude_gpt"},
		{"claude-opus-4-6-thinking", "claude_gpt"},
		{"gpt-oss-120b-medium", "claude_gpt"},
		{"unknown-custom-model", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := categorizeModelID(tt.modelID)
			if got != tt.expected {
				t.Errorf("categorizeModelID(%q) = %q; want %q", tt.modelID, got, tt.expected)
			}
		})
	}
}

func TestDoLSRequest_MockServer(t *testing.T) {
	expectedCSRF := "test-csrf-token"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-codeium-csrf-token") != expectedCSRF {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port

	ctx := context.Background()
	body, err := doLSRequest(ctx, expectedCSRF, []int{port}, "/", []byte("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var res map[string]string
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("failed to parse json: %v", err)
	}
	if res["status"] != "ok" {
		t.Errorf("expected status ok, got %v", res["status"])
	}
}

func TestExtractModels_Disambiguation(t *testing.T) {
	payload := &modelsPayload{
		Models: map[string]modelPayloadItem{
			"gemini-3.5-flash-lite": {
				DisplayName: "Gemini 3.5 Flash Lite",
			},
			"gemini-3.5-flash-lite-high": {
				DisplayName: "Gemini 3.5 Flash Lite",
			},
			"gemini-3.5-flash-lite-preview": {
				DisplayName: "Gemini 3.5 Flash Lite",
			},
			"claude-opus-5.5-high": {
				DisplayName: "Claude Opus 5.5 (High)",
			},
		},
	}

	models, err := extractModels(payload)
	if err != nil {
		t.Fatalf("extractModels failed: %v", err)
	}

	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d", len(models))
	}

	names := make(map[string]bool)
	for _, m := range models {
		if names[m.DisplayName] {
			t.Errorf("duplicate display name found after disambiguation: %s", m.DisplayName)
		}
		names[m.DisplayName] = true
	}

	// Verify specific variants were recognized
	if !names["Claude Opus 5.5 (High)"] {
		t.Error("expected Claude Opus 5.5 (High) to remain unchanged")
	}
	if !names["Gemini 3.5 Flash Lite (High)"] {
		t.Errorf("expected Gemini 3.5 Flash Lite (High), found: %+v", names)
	}
	if !names["Gemini 3.5 Flash Lite (Preview)"] {
		t.Errorf("expected Gemini 3.5 Flash Lite (Preview), found: %+v", names)
	}
}

func TestExtractModels_FiltersDisabledModels(t *testing.T) {
	payload := &modelsPayload{
		Models: map[string]modelPayloadItem{
			"gemini-3.8-flash-high": {
				DisplayName: "Gemini 3.8 Flash (High)",
				Recommended: true,
			},
			"gemini-4-argon-disabled": {
				DisplayName: "Gemini 4 Argon",
				Disabled:    true,
				Recommended: true,
			},
			"claude-opus-4.6": {
				DisplayName: "Claude Opus 4.6",
				Disabled:    false,
			},
		},
	}

	models, err := extractModels(payload)
	if err != nil {
		t.Fatalf("extractModels failed: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models after filtering disabled, got %d", len(models))
	}
	for _, m := range models {
		if strings.Contains(m.ID, "argon") || strings.Contains(m.ID, "disabled") {
			t.Errorf("expected disabled model to be filtered out, got: %+v", m)
		}
	}
}

func TestExtractModelVariant(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"gemini-4-hydrogen-xhigh", "XHigh"},
		{"gemini-4-hydrogen-high", "High"},
		{"gemini-4-hydrogen-medium", "Medium"},
		{"gemini-4-hydrogen-med", "Medium"},
		{"gemini-4-hydrogen-low", "Low"},
		{"gemini-4-hydrogen-minimal", "Minimal"},
		{"gemini-4-hydrogen-min", "Minimal"},
		{"gemini-4-hydrogen-think", "Thinking"},
		{"gemini-4-hydrogen-thinking", "Thinking"},
		{"gemini-4-hydrogen-xhigh-preview", "XHigh Preview"},
		{"gemini-4-hydrogen", ""},
	}
	for _, tt := range tests {
		got := extractModelVariant(tt.id)
		if got != tt.want {
			t.Errorf("extractModelVariant(%q) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

func TestExtractModels_DisambiguationAvoidsStackedParens(t *testing.T) {
	payload := &modelsPayload{
		Models: map[string]modelPayloadItem{
			"gemini-4-hydrogen-high": {
				DisplayName: "Gemini 4 Hydrogen (High)",
			},
			"gemini-4-hydrogen-high-alt": {
				DisplayName: "Gemini 4 Hydrogen (High)",
			},
		},
	}

	models, err := extractModels(payload)
	if err != nil {
		t.Fatalf("extractModels failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	seen := make(map[string]bool)
	for _, m := range models {
		if seen[m.DisplayName] {
			t.Errorf("duplicate display name: %s", m.DisplayName)
		}
		seen[m.DisplayName] = true
		if strings.Contains(m.DisplayName, ") (") {
			t.Errorf("stacked parentheses in display name %q", m.DisplayName)
		}
	}
}

func TestFetchAvailableModelsFromCloudCodeWithBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer expired-token":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		case "Bearer valid-token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":{"gemini-2.5-pro":{"displayName":"Gemini 2.5 Pro"}}}`))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	_, err := FetchAvailableModelsFromCloudCodeWithBaseURL(context.Background(), srv.URL, "expired-token")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	var statusErr *CloudCodeStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected *CloudCodeStatusError, got %T (%v)", err, err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected StatusCode 401, got %d", statusErr.StatusCode)
	}

	models, err := FetchAvailableModelsFromCloudCodeWithBaseURL(context.Background(), srv.URL, "valid-token")
	if err != nil {
		t.Fatalf("unexpected error for valid token: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gemini-2.5-pro" {
		t.Errorf("unexpected models: %+v", models)
	}
}

func TestCloudCodeStatusError_Error(t *testing.T) {
	withBody := &CloudCodeStatusError{StatusCode: 403, Body: "forbidden"}
	if !strings.Contains(withBody.Error(), "403") || !strings.Contains(withBody.Error(), "forbidden") {
		t.Errorf("unexpected Error() with body: %q", withBody.Error())
	}
	withoutBody := &CloudCodeStatusError{StatusCode: 500}
	if !strings.Contains(withoutBody.Error(), "500") {
		t.Errorf("unexpected Error() without body: %q", withoutBody.Error())
	}
}

func TestDoLSRequest_SuccessAndNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-codeium-csrf-token") != "test-csrf" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Extract port from srv.URL ("http://127.0.0.1:<port>")
	var port int
	_, _ = fmt.Sscanf(srv.URL, "http://127.0.0.1:%d", &port)
	if port == 0 {
		t.Fatalf("failed to parse port from %s", srv.URL)
	}

	body, err := doLSRequest(context.Background(), "test-csrf", []int{port}, "/ok", []byte(`{}`))
	if err != nil {
		t.Fatalf("doLSRequest(/ok) failed: %v", err)
	}
	if !strings.Contains(string(body), `"status":"ok"`) {
		t.Errorf("unexpected body: %s", string(body))
	}

	if _, err := doLSRequest(context.Background(), "test-csrf", []int{port}, "/fail", []byte(`{}`)); err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestDiscoverAvailableModels_EmptyToken(t *testing.T) {
	// Exercises DiscoverAvailableModels fallback path when no local LS or token is present.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = DiscoverAvailableModels(ctx, "")
}
