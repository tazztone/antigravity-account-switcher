package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDesktop(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	fakeBin := filepath.Join(tmpHome, "fake-antigravity")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeIcon := filepath.Join(tmpHome, "icon.png")
	if err := os.WriteFile(fakeIcon, []byte("fake-png-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := DesktopOptions{
		SwitcherBin:    "/usr/bin/antigravity-account-switcher",
		AntigravityBin: fakeBin,
		IconPath:       fakeIcon,
		Name:           "Antigravity 2.0 Test",
		Comment:        "Test Comment",
	}

	res, err := InstallDesktop(opts)
	if err != nil {
		t.Fatalf("InstallDesktop failed: %v", err)
	}

	if _, err := os.Stat(res.DesktopFilePath); err != nil {
		t.Errorf("expected desktop file at %s, got error %v", res.DesktopFilePath, err)
	}

	content, err := os.ReadFile(res.DesktopFilePath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(content), "Name=Antigravity 2.0 Test") {
		t.Errorf("expected Name in content, got:\n%s", string(content))
	}
	if !strings.Contains(string(content), "Exec=/usr/bin/antigravity-account-switcher launch %F") {
		t.Errorf("expected Exec launch in content, got:\n%s", string(content))
	}

	// Test Uninstall
	if err := UninstallDesktop(); err != nil {
		t.Fatalf("UninstallDesktop failed: %v", err)
	}
	if _, err := os.Stat(res.DesktopFilePath); !os.IsNotExist(err) {
		t.Errorf("expected desktop file to be removed, but still exists")
	}
}

func TestInstallDesktop_AntigravityIDE(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	fakeIDEBin := filepath.Join(tmpHome, "antigravity-ide")
	if err := os.WriteFile(fakeIDEBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := DesktopOptions{
		SwitcherBin:    "/usr/bin/antigravity-account-switcher",
		AntigravityBin: fakeIDEBin,
	}

	res, err := InstallDesktop(opts)
	if err != nil {
		t.Fatalf("InstallDesktop failed for IDE: %v", err)
	}

	if filepath.Base(res.DesktopFilePath) != "antigravity-ide.desktop" {
		t.Errorf("expected desktop filename 'antigravity-ide.desktop', got %s", filepath.Base(res.DesktopFilePath))
	}

	contentBytes, err := os.ReadFile(res.DesktopFilePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(contentBytes)

	if !strings.Contains(content, "Name=Antigravity IDE") {
		t.Errorf("expected default Name=Antigravity IDE, got:\n%s", content)
	}
	if !strings.Contains(content, "StartupWMClass=antigravity-ide") {
		t.Errorf("expected StartupWMClass=antigravity-ide, got:\n%s", content)
	}
	if !strings.Contains(content, "application/x-code-workspace") {
		t.Errorf("expected workspace MIME types, got:\n%s", content)
	}

	// Verify Uninstall cleans up IDE desktop file
	if err := UninstallDesktop(); err != nil {
		t.Fatalf("UninstallDesktop failed: %v", err)
	}
	if _, err := os.Stat(res.DesktopFilePath); !os.IsNotExist(err) {
		t.Errorf("expected IDE desktop file to be removed, but still exists")
	}
}
