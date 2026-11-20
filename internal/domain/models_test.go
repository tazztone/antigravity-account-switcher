package domain_test

import (
	"testing"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestIsInternalOrPreviewModel(t *testing.T) {
	cases := []struct {
		id       string
		expected bool
	}{
		{"chat_20706", true},
		{"chat_23310", true},
		{"tab_flash_lite_preview", true},
		{"tab_jump_flash_lite_preview", true},
		{"gemini-3.5-flash-lite-preview", true},
		{"gemini-3-flash", true},
		{"gemini-3.1-flash-image", true},
		{"gemini-3.5-flash-lite", true},
		{"gemini-3.1-flash-lite", true},
		{"gemini-2.5-pro", false},
		{"gemini-2.5-flash", false},
		{"gemini-3.8-flash-tiered", true},
		{"gemini-3.8-flash-high", false},
		{"gemini-3.8-flash-medium", false},
		{"gemini-3.8-flash-low", false},
		{"claude-opus-5.5-high", false},
		{"gpt-oss-120b-medium", false},
	}

	for _, tc := range cases {
		got := domain.IsInternalOrPreviewModel(tc.id)
		if got != tc.expected {
			t.Errorf("IsInternalOrPreviewModel(%q) = %v; want %v", tc.id, got, tc.expected)
		}
	}
}

func TestModelInfo_IsInternalOrPreview(t *testing.T) {
	var nilInfo *domain.ModelInfo
	if nilInfo.IsInternalOrPreview() {
		t.Error("expected nil ModelInfo.IsInternalOrPreview() to be false")
	}
	internal := &domain.ModelInfo{ID: "chat_20706"}
	if !internal.IsInternalOrPreview() {
		t.Error("expected chat_20706 to be internal")
	}
	regular := &domain.ModelInfo{ID: "gemini-2.5-pro"}
	if regular.IsInternalOrPreview() {
		t.Error("expected gemini-2.5-pro not to be internal")
	}
}

func TestCategorizeModelID(t *testing.T) {
	cases := []struct {
		model string
		want  domain.ModelCategory
	}{
		{"", domain.CategoryUnknown},
		{"   ", domain.CategoryUnknown},
		{"models/claude-3-5-sonnet", domain.CategoryClaudeGPT},
		{"MODELS/o3-mini", domain.CategoryClaudeGPT},
		{"MODELS/O1_preview", domain.CategoryClaudeGPT},
		{"o1", domain.CategoryClaudeGPT},
		{"o3", domain.CategoryClaudeGPT},
		{"o4", domain.CategoryClaudeGPT},
		{"gpt-4o", domain.CategoryClaudeGPT},
		{"anthropic-claude-opus", domain.CategoryClaudeGPT},
		{"openai-codex", domain.CategoryClaudeGPT},
		{"chatgpt-4o-latest", domain.CategoryClaudeGPT},
		{"haiku-3.5", domain.CategoryClaudeGPT},
		{"3p-partner-model", domain.CategoryClaudeGPT},
		{"gemini-4-argon", domain.CategoryGeminiArgon},
		{"MODELS/GEMINI-ARGON-PRO", domain.CategoryGeminiArgon},
		{"gemini-2.5-pro", domain.CategoryGemini},
		{"gemma-3-27b", domain.CategoryGemini},
		{"flash-lite", domain.CategoryGemini},
		{"pro-model", domain.CategoryGemini},
		{"ultra-model", domain.CategoryGemini},
		{"unknown-custom-model", domain.CategoryUnknown},
		// >64B heap path
		{"MODELS/very-long-model-identifier-exceeding-sixty-four-bytes-for-heap-path-claude", domain.CategoryClaudeGPT},
		{"very-long-model-identifier-exceeding-sixty-four-bytes-for-heap-path-gemini-pro", domain.CategoryGemini},
	}

	for _, tc := range cases {
		if got := domain.CategorizeModelID(tc.model); got != tc.want {
			t.Errorf("CategorizeModelID(%q) = %v; want %v", tc.model, got, tc.want)
		}
	}
}
