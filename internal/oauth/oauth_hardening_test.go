package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/domain"
	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/store/sqlite"
)

func TestEnsureValidToken_NilRepoAndDefaultExpiresIn(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":0,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	svc := NewOAuthService(nil, WithTokenURL(srv.URL))
	acc := &domain.Account{
		ID:           "acc-nil-repo",
		Email:        "nil@example.com",
		RefreshToken: "rt-1",
		TokenExpiry:  time.Now().Add(-1 * time.Hour),
	}

	got, err := svc.EnsureValidToken(context.Background(), acc, 60*time.Second)
	if err != nil {
		t.Fatalf("EnsureValidToken: %v", err)
	}
	if got.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want new-access", got.AccessToken)
	}
	if got.TokenExpiry.Before(time.Now().Add(30 * time.Minute)) {
		t.Errorf("expected default 3600s expiry, got %v", got.TokenExpiry)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 refresh call, got %d", calls.Load())
	}
}

func TestAutoImportFromACP_InvalidJSONAndMissingRefreshToken(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	acpDir := filepath.Join(tmpDir, ".gemini", "antigravity-acp")
	if err := os.MkdirAll(acpDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()
	accRepo := sqlite.NewAccountRepository(db)
	svc := NewOAuthService(nil)

	// Invalid JSON
	_ = os.WriteFile(filepath.Join(acpDir, "acp_token.json"), []byte(`{invalid`), 0o600)
	if _, err := AutoImportExistingAccount(context.Background(), accRepo, svc); err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Missing refresh_token
	_ = os.WriteFile(filepath.Join(acpDir, "acp_token.json"), []byte(`{"access_token":"only"}`), 0o600)
	if _, err := AutoImportExistingAccount(context.Background(), accRepo, svc); err == nil {
		t.Error("expected error for missing refresh_token")
	}
}
