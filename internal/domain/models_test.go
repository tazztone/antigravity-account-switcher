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
		{"gemini-2.5-pro", false},
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
