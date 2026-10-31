package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPort                     = 8080
	DefaultUpstreamURL              = "https://daily-cloudcode-pa.googleapis.com"
	DefaultInterval                 = "5m"
	ConfigFileName                  = "config.json"
	DefaultDBFileName               = "accounts.db"
	DefaultModelPrimary             = "gemini-2.5-pro"
	DefaultModelSecondary           = "claude-3-5-sonnet"
	DefaultFallbackSecondaryEnabled = false
	DefaultMaxBodyMB                = 100
	FallbackStrategyPoolFirst       = "pool_first"
	FallbackStrategyAccountFirst    = "account_first"
	DefaultFallbackStrategy         = FallbackStrategyPoolFirst
)

// Config holds persistent user configuration.
type Config struct {
	Port                     int    `json:"port"`
	DBPath                   string `json:"db_path"`
	AntigravityBin           string `json:"antigravity_bin"`
	UpstreamURL              string `json:"upstream_url"`
	QuotaInterval            string `json:"quota_interval"`
	OpenBrowser              bool   `json:"open_browser"`
	ModelPrimary             string `json:"model_primary"`
	ModelSecondary           string `json:"model_secondary"`
	FallbackSecondaryEnabled bool   `json:"fallback_secondary_enabled"`
	FallbackStrategy         string `json:"fallback_strategy,omitempty"`
	MaxBodyMB                int    `json:"max_body_mb,omitempty"`
	ClientID                 string `json:"client_id,omitempty"`
	ClientSecret             string `json:"client_secret,omitempty"`
}

// ConfigDir returns the default configuration directory (~/.config/antigravity-account-switcher).
func ConfigDir() string {
	if custom := os.Getenv("ANTIGRAVITY_CONFIG_DIR"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".antigravity-account-switcher"
	}
	return filepath.Join(home, ".config", "antigravity-account-switcher")
}

// ConfigFilePath returns the absolute path to the configuration file.
func ConfigFilePath() string {
	return filepath.Join(ConfigDir(), ConfigFileName)
}

// DefaultDBPath returns the default SQLite database path.
func DefaultDBPath() string {
	if env := os.Getenv("ANTIGRAVITY_DB_PATH"); env != "" {
		return env
	}
	return filepath.Join(ConfigDir(), DefaultDBFileName)
}

// DefaultConfig returns an initialized Config struct with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Port:                     DefaultPort,
		DBPath:                   DefaultDBPath(),
		AntigravityBin:           "",
		UpstreamURL:              DefaultUpstreamURL,
		QuotaInterval:            DefaultInterval,
		OpenBrowser:              false,
		ModelPrimary:             DefaultModelPrimary,
		ModelSecondary:           DefaultModelSecondary,
		FallbackSecondaryEnabled: DefaultFallbackSecondaryEnabled,
		FallbackStrategy:         DefaultFallbackStrategy,
		MaxBodyMB:                DefaultMaxBodyMB,
	}
}

// LoadDiskConfig reads the configuration strictly from disk without applying environment variable overrides.
func LoadDiskConfig() (*Config, error) {
	cfg := DefaultConfig()
	path := ConfigFilePath()

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read config file at %s: %w", path, err)
	}

	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config JSON at %s: %w", path, err)
		}
	}

	// Defensive defaults if unmarshaled JSON contained explicit empty strings or invalid limits
	if cfg.ModelPrimary == "" {
		cfg.ModelPrimary = DefaultModelPrimary
	}
	if cfg.ModelSecondary == "" {
		cfg.ModelSecondary = DefaultModelSecondary
	}
	if cfg.FallbackStrategy == "" {
		cfg.FallbackStrategy = DefaultFallbackStrategy
	}
	if cfg.MaxBodyMB <= 0 {
		cfg.MaxBodyMB = DefaultMaxBodyMB
	}

	return cfg, nil
}

// Load reads the configuration from disk, applying environment variable overrides.
func Load() (*Config, error) {
	cfg, err := LoadDiskConfig()
	if err != nil {
		return nil, err
	}

	// Environment variable overrides
	if envPort := os.Getenv("ANTIGRAVITY_PORT"); envPort != "" {
		var p int
		if _, err := fmt.Sscanf(envPort, "%d", &p); err == nil && p > 0 {
			cfg.Port = p
		}
	}
	if envDB := os.Getenv("ANTIGRAVITY_DB_PATH"); envDB != "" {
		cfg.DBPath = envDB
	}
	if envBin := os.Getenv("ANTIGRAVITY_BIN"); envBin != "" {
		cfg.AntigravityBin = envBin
	}
	if envUpstream := os.Getenv("ANTIGRAVITY_UPSTREAM_URL"); envUpstream != "" {
		cfg.UpstreamURL = envUpstream
	}
	if envPrimary := os.Getenv("ANTIGRAVITY_MODEL_PRIMARY"); envPrimary != "" {
		if trimmed := strings.TrimSpace(envPrimary); trimmed != "" {
			cfg.ModelPrimary = trimmed
		}
	}
	if envSecondary := os.Getenv("ANTIGRAVITY_MODEL_SECONDARY"); envSecondary != "" {
		if trimmed := strings.TrimSpace(envSecondary); trimmed != "" {
			cfg.ModelSecondary = trimmed
		}
	}
	if envFallback := os.Getenv("ANTIGRAVITY_FALLBACK_SECONDARY_ENABLED"); envFallback != "" {
		if b, err := ParseBool(envFallback); err == nil {
			cfg.FallbackSecondaryEnabled = b
		}
	}
	if envStrategy := os.Getenv("ANTIGRAVITY_FALLBACK_STRATEGY"); envStrategy != "" {
		if trimmed := strings.TrimSpace(envStrategy); trimmed != "" {
			cfg.FallbackStrategy = trimmed
		}
	}
	if envClientID := os.Getenv("ANTIGRAVITY_CLIENT_ID"); envClientID != "" {
		cfg.ClientID = strings.TrimSpace(envClientID)
	}
	if envClientSecret := os.Getenv("ANTIGRAVITY_CLIENT_SECRET"); envClientSecret != "" {
		cfg.ClientSecret = strings.TrimSpace(envClientSecret)
	}
	if envMaxBody := os.Getenv("ANTIGRAVITY_MAX_BODY_MB"); envMaxBody != "" {
		if mb, err := strconv.Atoi(strings.TrimSpace(envMaxBody)); err == nil && mb > 0 {
			cfg.MaxBodyMB = mb
		}
	}

	return cfg, nil
}

// Save writes the configuration to disk, ensuring directory creation and restrictive 0600 file permissions.
func Save(cfg *Config) error {
	dir := ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}

	path := ConfigFilePath()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config to %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)

	return nil
}

// ParseBool parses string representations of boolean values.
// Recognizes truthy values: "1", "t", "true", "yes", "y", "on" (case-insensitive).
// Recognizes falsy values: "0", "f", "false", "no", "n", "off" (case-insensitive).
// Any other string returns an error.
func ParseBool(val string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "t", "true", "yes", "y", "on":
		return true, nil
	case "0", "f", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("cannot parse %q as boolean", val)
	}
}

// MaxBodyBytes returns the maximum request body buffer size in bytes.
// If MaxBodyMB is <= 0 or the receiver is nil, it falls back to DefaultMaxBodyMB in bytes.
func (c *Config) MaxBodyBytes() int64 {
	if c == nil || c.MaxBodyMB <= 0 {
		return int64(DefaultMaxBodyMB) * 1024 * 1024
	}
	return int64(c.MaxBodyMB) * 1024 * 1024
}

// Validate verifies configuration boundaries, network URLs, and model fallback invariants.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d: must be between 1 and 65535", c.Port)
	}
	if c.MaxBodyMB < 0 {
		return fmt.Errorf("invalid max_body_mb %d: cannot be negative", c.MaxBodyMB)
	}
	if c.UpstreamURL != "" {
		parsed, err := url.Parse(c.UpstreamURL)
		if err != nil {
			return fmt.Errorf("invalid upstream_url %q: %w", c.UpstreamURL, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("invalid upstream_url %q: scheme must be http or https", c.UpstreamURL)
		}
	}
	if c.QuotaInterval != "" {
		if _, err := time.ParseDuration(c.QuotaInterval); err != nil {
			return fmt.Errorf("invalid quota_interval %q: %w", c.QuotaInterval, err)
		}
	}
	if c.FallbackSecondaryEnabled {
		primary := strings.TrimSpace(c.ModelPrimary)
		secondary := strings.TrimSpace(c.ModelSecondary)
		if primary == "" {
			return errors.New("model_primary cannot be empty when fallback_secondary_enabled is true")
		}
		if secondary == "" {
			return errors.New("model_secondary cannot be empty when fallback_secondary_enabled is true")
		}
		if strings.EqualFold(primary, secondary) {
			return fmt.Errorf("model_primary and model_secondary cannot be identical (%q)", c.ModelPrimary)
		}
	}
	if c.FallbackStrategy != "" && c.FallbackStrategy != FallbackStrategyPoolFirst && c.FallbackStrategy != FallbackStrategyAccountFirst {
		return fmt.Errorf("invalid fallback_strategy %q: must be %q or %q", c.FallbackStrategy, FallbackStrategyPoolFirst, FallbackStrategyAccountFirst)
	}
	return nil
}

// CandidateAntigravityPaths returns standard probing locations across Linux and macOS.
func CandidateAntigravityPaths() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{}

	if home != "" {
		candidates = append(candidates,
			// 1. Standard XDG user application directories (Recommended, no sudo)
			filepath.Join(home, ".local", "bin", "antigravity-ide"),
			filepath.Join(home, ".local", "bin", "antigravity"),
			filepath.Join(home, ".local", "share", "antigravity-ide", "antigravity-ide"),
			filepath.Join(home, ".local", "share", "antigravity-ide", "Antigravity-IDE", "antigravity-ide"),
			filepath.Join(home, ".local", "share", "antigravity", "antigravity"),
			filepath.Join(home, ".local", "share", "antigravity", "Antigravity-x64", "antigravity"),
			filepath.Join(home, ".local", "share", "Antigravity", "antigravity"),
			filepath.Join(home, ".local", "share", "Antigravity", "Antigravity-x64", "antigravity"),
			// 2. Custom tools & user development directories
			filepath.Join(home, "tools", "Antigravity-IDE", "antigravity-ide"),
			filepath.Join(home, "tools", "Antigravity-IDE", "Antigravity-IDE", "antigravity-ide"),
			filepath.Join(home, "tools", "antigravity-ide", "antigravity-ide"),
			filepath.Join(home, "tools", "antigravity-ide", "Antigravity-IDE", "antigravity-ide"),
			filepath.Join(home, "tools", "Antigravity", "Antigravity-x64", "antigravity"),
			filepath.Join(home, "tools", "Antigravity", "antigravity"),
			filepath.Join(home, "tools", "antigravity", "Antigravity-x64", "antigravity"),
			filepath.Join(home, "tools", "antigravity", "antigravity"),
			filepath.Join(home, "tools", "Antigravity-2.0", "antigravity"),
			filepath.Join(home, "Antigravity", "antigravity"),
			filepath.Join(home, "Antigravity", "Antigravity-x64", "antigravity"),
			filepath.Join(home, ".antigravity", "bin", "antigravity"),
			// 3. macOS Application Support
			filepath.Join(home, "Applications", "Antigravity.app", "Contents", "MacOS", "Antigravity"),
		)
	}

	// 4. System-wide FHS locations (Installed via sudo into /opt or /usr/local/bin)
	candidates = append(candidates,
		"/usr/local/bin/antigravity-ide",
		"/usr/local/bin/antigravity",
		"/opt/antigravity-ide/antigravity-ide",
		"/opt/antigravity-ide/Antigravity-IDE/antigravity-ide",
		"/opt/antigravity/antigravity",
		"/opt/antigravity/Antigravity-x64/antigravity",
		"/opt/Antigravity/antigravity",
		"/opt/Antigravity/Antigravity-x64/antigravity",
		"/Applications/Antigravity.app/Contents/MacOS/Antigravity",
	)

	// 5. User CLI fallback (only probed if no GUI IDE / app binary is found)
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "agy"),
		)
	}

	return candidates
}

// ResolveAntigravityBin determines the binary to run following priority:
// 1. Explicit override argument (if provided)
// 2. ANTIGRAVITY_BIN environment variable
// 3. Saved config file setting (`antigravity_bin`)
// 4. Candidate probing paths in filesystem (IDE and Desktop apps prioritized before CLI)
// 5. PATH lookups (`antigravity-ide`, `antigravity`, then `agy`)
func ResolveAntigravityBin(explicitOverride string) (string, error) {
	if explicitOverride != "" {
		if _, err := os.Stat(explicitOverride); err == nil {
			return filepath.Abs(explicitOverride)
		}
		// Check if it's a command in PATH
		if path, err := exec.LookPath(explicitOverride); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("specified antigravity binary not found: %s", explicitOverride)
	}

	if env := os.Getenv("ANTIGRAVITY_BIN"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return filepath.Abs(env)
		}
		if path, err := exec.LookPath(env); err == nil {
			return path, nil
		}
	}

	cfg, _ := Load()
	if cfg != nil && cfg.AntigravityBin != "" {
		if _, err := os.Stat(cfg.AntigravityBin); err == nil {
			return filepath.Abs(cfg.AntigravityBin)
		}
		if path, err := exec.LookPath(cfg.AntigravityBin); err == nil {
			return path, nil
		}
	}

	// Probe candidate filesystem locations
	for _, candidate := range CandidateAntigravityPaths() {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			if fi.Mode()&0o111 != 0 {
				return filepath.Abs(candidate)
			}
		}
	}

	// Look up in PATH
	for _, cmdName := range []string{"antigravity-ide", "antigravity", "agy"} {
		if path, err := exec.LookPath(cmdName); err == nil {
			return path, nil
		}
	}

	return "", errors.New("could not automatically locate Antigravity binary; specify it via 'antigravity-account-switcher config set antigravity_bin <path>' or flag '--bin'")
}

// FindAntigravityIcon searches for an icon in the directory of the resolved binary.
func FindAntigravityIcon(resolvedBin string) string {
	if resolvedBin == "" {
		return ""
	}

	dir := filepath.Dir(resolvedBin)
	searchDirs := []string{
		dir,
		filepath.Dir(dir), // Parent dir (e.g. ~/tools/Antigravity if bin is in Antigravity-x64)
		filepath.Join(dir, "resources"),
	}

	for _, d := range searchDirs {
		for _, name := range []string{"icon.png", "logo.png", "antigravity.png", "antigravity-ide.png"} {
			p := filepath.Join(d, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
				return p
			}
		}
	}

	return ""
}

var (
	detectedVersionMu sync.RWMutex
	detectedVersion   string
	versionRegex      = regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)
)

// DetectAntigravityCLIVersion determines the Antigravity / AGY CLI semantic version.
// It probes the resolved binary with '--version'. If detection fails or returns a
// version earlier than "1.4.10", it defaults to "1.4.10" so that Google Cloud Code PA
// enables Gemini 4 Argon quota and models.
func DetectAntigravityCLIVersion(overrideBin string) string {
	detectedVersionMu.RLock()
	if detectedVersion != "" && overrideBin == "" {
		v := detectedVersion
		detectedVersionMu.RUnlock()
		return v
	}
	detectedVersionMu.RUnlock()

	const minSupportedVersion = "1.4.10"
	targetVersion := minSupportedVersion

	bin, err := ResolveAntigravityBin(overrideBin)
	if err == nil && bin != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "--version")
		cmd.WaitDelay = 500 * time.Millisecond
		if out, cmdErr := cmd.Output(); cmdErr == nil {
			trimmed := strings.TrimSpace(string(out))
			if m := versionRegex.FindString(trimmed); m != "" {
				if compareSemver(m, minSupportedVersion) >= 0 {
					targetVersion = m
				}
			}
		}
	}

	if overrideBin == "" {
		detectedVersionMu.Lock()
		detectedVersion = targetVersion
		detectedVersionMu.Unlock()
	}

	return targetVersion
}

// compareSemver compares two semantic version strings ("X.Y.Z").
// Returns 1 if v1 > v2, -1 if v1 < v2, and 0 if v1 == v2.
func compareSemver(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i])
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

// AntigravityUserAgent returns a User-Agent string simulating the Antigravity CLI
// with at least version 1.4.10, ensuring Google Cloud Code PA exposes the Gemini 4 Argon tier.
func AntigravityUserAgent(overrideBin string) string {
	ver := DetectAntigravityCLIVersion(overrideBin)
	return fmt.Sprintf("antigravity/cli/%s (aidev_client; os_type=%s; arch=%s; cl=976013059; auth_method=consumer)", ver, runtime.GOOS, runtime.GOARCH)
}
