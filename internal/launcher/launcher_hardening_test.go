package launcher

import (
	"strings"
	"testing"
)

func TestBuildScopedEnv_MergesExistingNoProxy(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"NO_PROXY=corp.internal,10.0.0.0/8,localhost",
	}
	env := BuildScopedEnv(base, "http://127.0.0.1:8080")
	var noProxy string
	for _, kv := range env {
		if strings.HasPrefix(kv, "NO_PROXY=") {
			noProxy = strings.TrimPrefix(kv, "NO_PROXY=")
		}
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "speech.googleapis.com", "corp.internal", "10.0.0.0/8"} {
		if !strings.Contains(noProxy, want) {
			t.Errorf("NO_PROXY %q missing %q", noProxy, want)
		}
	}
}

func TestQuoteDesktopExecPath(t *testing.T) {
	if got := quoteDesktopExecPath("/usr/local/bin/switcher"); got != "/usr/local/bin/switcher" {
		t.Errorf("unquoted path = %q", got)
	}
	if got := quoteDesktopExecPath("/opt/My App/switcher"); got != `"/opt/My App/switcher"` {
		t.Errorf("quoted path = %q", got)
	}
}

func TestWithModelFallback_SetsFallbackConfigured(t *testing.T) {
	var cfg Config
	WithModelFallback("p", "s", true)(&cfg)
	if !cfg.fallbackConfigured || !cfg.FallbackSecondaryEnabled {
		t.Errorf("expected fallbackConfigured=true and FallbackSecondaryEnabled=true, got %+v", cfg)
	}
}
