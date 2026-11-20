package domain_test

import (
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
)

func TestModelCategory_String(t *testing.T) {
	cases := []struct {
		cat  domain.ModelCategory
		want string
	}{
		{domain.CategoryGemini, "gemini"},
		{domain.CategoryGeminiArgon, "gemini_argon"},
		{domain.CategoryClaudeGPT, "claude_gpt"},
		{domain.CategoryUnknown, "unknown"},
		{domain.ModelCategory("custom"), "custom"},
	}

	for _, tc := range cases {
		if got := tc.cat.String(); got != tc.want {
			t.Errorf("ModelCategory(%q).String() = %q; want %q", tc.cat, got, tc.want)
		}
	}
}

func TestAccount_IsCategoryDisabled(t *testing.T) {
	acc := &domain.Account{
		DisabledCategories: []string{"gemini", "claude_gpt"},
	}

	if !acc.IsCategoryDisabled(domain.CategoryGemini) {
		t.Error("expected CategoryGemini to be disabled")
	}
	if !acc.IsCategoryDisabled(domain.CategoryClaudeGPT) {
		t.Error("expected CategoryClaudeGPT to be disabled")
	}
	if acc.IsCategoryDisabled(domain.CategoryGeminiArgon) {
		t.Error("expected CategoryGeminiArgon not to be disabled")
	}
	if acc.IsCategoryDisabled(domain.CategoryUnknown) {
		t.Error("expected CategoryUnknown not to be disabled")
	}

	emptyAcc := &domain.Account{}
	if emptyAcc.IsCategoryDisabled(domain.CategoryGemini) {
		t.Error("expected empty DisabledCategories not to disable CategoryGemini")
	}
}

func TestAccount_IsTokenExpired(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero expiry", time.Time{}, true},
		{"past expiry", now.Add(-10 * time.Minute), true},
		{"within 60s buffer", now.Add(30 * time.Second), true},
		{"future beyond buffer", time.Now().Add(10 * time.Minute), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := &domain.Account{AccessToken: "tok", TokenExpiry: tc.expiry}
			if got := acc.IsTokenExpired(60 * time.Second); got != tc.want {
				t.Errorf("IsTokenExpired(60s) = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestAccount_IsAvailable(t *testing.T) {
	cases := []struct {
		status domain.AccountStatus
		want   bool
	}{
		{domain.AccountStatusActive, true},
		{domain.AccountStatusExhausted, false},
		{domain.AccountStatusDisabled, false},
		{domain.AccountStatusError, false},
	}

	for _, tc := range cases {
		acc := &domain.Account{Status: tc.status}
		if got := acc.IsAvailable(); got != tc.want {
			t.Errorf("Account{Status: %q}.IsAvailable() = %v; want %v", tc.status, got, tc.want)
		}
	}
}

func TestQuotaBucket_IsExhaustedAndHasReset(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name          string
		bucket        domain.QuotaBucket
		wantExhausted bool
		wantHasReset  bool
	}{
		{
			name:          "zero remaining and past reset",
			bucket:        domain.QuotaBucket{RemainingFraction: 0.0, ResetTime: now.Add(-1 * time.Minute)},
			wantExhausted: true,
			wantHasReset:  true,
		},
		{
			name:          "negative remaining and future reset",
			bucket:        domain.QuotaBucket{RemainingFraction: -0.1, ResetTime: now.Add(10 * time.Minute)},
			wantExhausted: true,
			wantHasReset:  false,
		},
		{
			name:          "positive remaining and zero reset",
			bucket:        domain.QuotaBucket{RemainingFraction: 0.5, ResetTime: time.Time{}},
			wantExhausted: false,
			wantHasReset:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.bucket.IsExhausted(); got != tc.wantExhausted {
				t.Errorf("IsExhausted() = %v; want %v", got, tc.wantExhausted)
			}
			if got := tc.bucket.HasReset(now); got != tc.wantHasReset {
				t.Errorf("HasReset(now) = %v; want %v", got, tc.wantHasReset)
			}
		})
	}
}
