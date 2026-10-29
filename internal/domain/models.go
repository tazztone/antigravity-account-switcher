package domain

import (
	"strings"
	"time"
)

// ModelInfo represents an AI model available in Google Antigravity 2.0.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"` // "gemini", "gemini_argon" or "claude_gpt"
	Recommended bool   `json:"recommended"`
}

// IsInternalOrPreview returns true if the model represents an internal, tab, or experimental endpoint.
func (m *ModelInfo) IsInternalOrPreview() bool {
	if m == nil {
		return false
	}
	return IsInternalOrPreviewModel(m.ID)
}

// IsInternalOrPreviewModel checks if a model ID is an internal, tab, tool, or preview endpoint.
func IsInternalOrPreviewModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	if len(lower) >= 4 && (strings.HasPrefix(lower, "chat_") || strings.HasPrefix(lower, "tab_") || strings.Contains(lower, "preview")) {
		return true
	}
	// Internal task, tool, command, and non-agent models
	if lower == "gemini-3-flash" || strings.Contains(lower, "flash-image") ||
		strings.HasPrefix(lower, "gemini-3.5-flash") || strings.HasPrefix(lower, "gemini-3.1-flash") ||
		strings.HasSuffix(lower, "-tiered") {
		return true
	}
	return false
}

// AccountModelRule captures per-account model eligibility.
type AccountModelRule struct {
	AccountID   string    `json:"account_id"`
	ModelID     string    `json:"model_id"`
	DisplayName string    `json:"display_name"`
	Category    string    `json:"category"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}
