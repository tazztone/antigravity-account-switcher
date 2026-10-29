package quota

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
