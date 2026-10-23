package domain

import "time"

// ModelInfo represents an AI model available in Google Antigravity 2.0.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"` // "gemini", "gemini_argon" or "claude_gpt"
	Recommended bool   `json:"recommended"`
}

// FallbackAccountID sentinel values for AccountModelRule.
const (
	// FallbackAccountAuto lets the proxy pick the next eligible account in the pool.
	FallbackAccountAuto = ""
	// FallbackAccountNone blocks account rotation for an unsupported model and surfaces the upstream error.
	FallbackAccountNone = "none"
)

// AccountModelRule captures per-account model eligibility and fallback routing.
type AccountModelRule struct {
	AccountID         string    `json:"account_id"`
	ModelID           string    `json:"model_id"`
	DisplayName       string    `json:"display_name"`
	Category          string    `json:"category"`
	Enabled           bool      `json:"enabled"`
	FallbackAccountID string    `json:"fallback_account_id"` // "" = auto eligible pool, "none" = block rotation, "<acc_id>" = specific account
	UpdatedAt         time.Time `json:"updated_at"`
}
