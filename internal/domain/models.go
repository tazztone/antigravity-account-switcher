package domain

import (
	"strings"
	"time"
	"unsafe"
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

// CategorizeModelID maps any model identifier to its canonical top-level ModelCategory.
// Zero heap allocations via stack buffer ASCII lowercase.
func CategorizeModelID(model string) ModelCategory {
	m := strings.TrimSpace(model)
	if m == "" {
		return CategoryUnknown
	}
	m = strings.TrimPrefix(m, "models/")

	// Fast stack-allocated ASCII lowercase (0 heap allocs)
	var buf [64]byte
	var lower string
	if len(m) <= len(buf) {
		hasUpper := false
		for i := 0; i < len(m); i++ {
			c := m[i]
			if c >= 'A' && c <= 'Z' {
				buf[i] = c + ('a' - 'A')
				hasUpper = true
			} else {
				buf[i] = c
			}
		}
		if hasUpper {
			lower = unsafe.String(&buf[0], len(m))
		} else {
			lower = m
		}
	} else {
		lower = strings.ToLower(m)
	}

	// 1. Claude & GPT Family Check (evaluated first to prevent "pro" collision)
	if strings.Contains(lower, "claude") ||
		strings.Contains(lower, "gpt") ||
		strings.Contains(lower, "sonnet") ||
		strings.Contains(lower, "opus") ||
		strings.Contains(lower, "haiku") ||
		strings.Contains(lower, "anthropic") ||
		strings.Contains(lower, "openai") ||
		strings.Contains(lower, "chatgpt") ||
		strings.Contains(lower, "3p") ||
		lower == "o1" || lower == "o3" || lower == "o4" ||
		strings.HasPrefix(lower, "o1-") || strings.HasPrefix(lower, "o3-") || strings.HasPrefix(lower, "o4-") ||
		strings.HasPrefix(lower, "o1_") || strings.HasPrefix(lower, "o3_") || strings.HasPrefix(lower, "o4_") {
		return CategoryClaudeGPT
	}

	// 2. Gemini 4 Argon Check (before generic gemini to avoid bucket collision)
	if strings.Contains(lower, "argon") {
		return CategoryGeminiArgon
	}

	// 3. Gemini Family Check
	if strings.Contains(lower, "gemini") ||
		strings.Contains(lower, "gemma") ||
		strings.Contains(lower, "flash") ||
		strings.Contains(lower, "pro") ||
		strings.Contains(lower, "ultra") {
		return CategoryGemini
	}

	return CategoryUnknown
}
