package launcher

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Muriel-Gasparini/antigravity-account-switcher/internal/config"
)

// DesktopOptions configures the generation of the .desktop application entry.
type DesktopOptions struct {
	SwitcherBin    string
	AntigravityBin string
	IconPath       string
	Name           string
	Comment        string
	Force          bool
}

// Result describes the installed desktop entry files.
type DesktopResult struct {
	DesktopFilePath string
	IconFilePath    string
	AntigravityBin  string
	SwitcherBin     string
}

// InstallDesktop installs a native GNOME/XDG .desktop entry for Antigravity 2.0.
func InstallDesktop(opts DesktopOptions) (*DesktopResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get user home directory: %w", err)
	}

	// 1. Resolve Switcher Binary
	switcherBin := opts.SwitcherBin
	if switcherBin == "" {
		if exe, err := os.Executable(); err == nil {
			switcherBin, _ = filepath.Abs(exe)
		} else {
			switcherBin = "antigravity-account-switcher"
		}
	}

	// 2. Resolve Antigravity Binary
	antigravityBin := opts.AntigravityBin
	if antigravityBin == "" {
		resolved, err := config.ResolveAntigravityBin("")
		if err != nil {
			return nil, fmt.Errorf("failed to locate Antigravity binary: %w", err)
		}
		antigravityBin = resolved
	}

	// Persist resolved Antigravity binary to config for future launches
	if cfg, err := config.Load(); err == nil {
		cfg.AntigravityBin = antigravityBin
		_ = config.Save(cfg)
	}

	binBase := strings.ToLower(filepath.Base(antigravityBin))
	isIDE := strings.Contains(binBase, "antigravity-ide")

	desktopFileName := "antigravity.desktop"
	defaultName := "Antigravity 2.0"
	defaultComment := "Google Antigravity 2.0 with Multi-Account Switcher"
	defaultWMClass := "Antigravity"
	defaultMimeType := "text/plain;inode/directory;"
	iconFileName := "antigravity.png"

	if isIDE {
		desktopFileName = "antigravity-ide.desktop"
		defaultName = "Antigravity IDE"
		defaultComment = "Google Antigravity IDE with Multi-Account Switcher"
		defaultWMClass = "antigravity-ide"
		defaultMimeType = "inode/directory;text/plain;application/x-code-workspace;application/x-antigravity-workspace;x-scheme-handler/antigravity-ide;"
		iconFileName = "antigravity-ide.png"
	}

	// 3. Resolve and copy Icon
	iconSrc := opts.IconPath
	if iconSrc == "" {
		iconSrc = config.FindAntigravityIcon(antigravityBin)
	}

	iconsDir := filepath.Join(home, ".local", "share", "icons")
	_ = os.MkdirAll(iconsDir, 0o755)

	destIcon := filepath.Join(iconsDir, iconFileName)
	if iconSrc != "" && iconSrc != destIcon {
		if err := copyFile(iconSrc, destIcon); err != nil {
			// Non-fatal fallback to iconSrc directly
			destIcon = iconSrc
		}
	} else if iconSrc == "" {
		if isIDE {
			destIcon = "antigravity-ide"
		} else {
			destIcon = "antigravity"
		}
	}

	// 4. Write .desktop file
	appsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create applications directory %s: %w", appsDir, err)
	}

	desktopFile := filepath.Join(appsDir, desktopFileName)

	name := opts.Name
	if name == "" {
		name = defaultName
	}
	comment := opts.Comment
	if comment == "" {
		comment = defaultComment
	}

	content := fmt.Sprintf(`[Desktop Entry]
Name=%s
Comment=%s
Exec=%s launch %%F
Icon=%s
Terminal=false
Type=Application
Categories=Development;IDE;
StartupWMClass=%s
MimeType=%s
Actions=new-empty-window;

[Desktop Action new-empty-window]
Name=New Empty Window
Exec=%s launch --new-window
Icon=%s
`, name, comment, switcherBin, destIcon, defaultWMClass, defaultMimeType, switcherBin, destIcon)

	if err := os.WriteFile(desktopFile, []byte(content), 0o755); err != nil {
		return nil, fmt.Errorf("failed to write desktop file to %s: %w", desktopFile, err)
	}

	// Try updating the desktop database if the tool is installed
	if updateBin, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(updateBin, appsDir).Run()
	}

	// Try updating the user mime database to eliminate stale/invalid cache warnings
	if updateMimeBin, err := exec.LookPath("update-mime-database"); err == nil {
		userMimeDir := filepath.Join(home, ".local", "share", "mime")
		if fi, err := os.Stat(userMimeDir); err == nil && fi.IsDir() {
			_ = exec.Command(updateMimeBin, userMimeDir).Run()
		}
	}

	return &DesktopResult{
		DesktopFilePath: desktopFile,
		IconFilePath:    destIcon,
		AntigravityBin:  antigravityBin,
		SwitcherBin:     switcherBin,
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// UninstallDesktop removes the installed .desktop entry and updates the system desktop database.
func UninstallDesktop() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	appsDir := filepath.Join(home, ".local", "share", "applications")
	for _, f := range []string{"antigravity.desktop", "antigravity-ide.desktop"} {
		desktopFile := filepath.Join(appsDir, f)
		_ = os.Remove(desktopFile)
	}

	if updateBin, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(updateBin, appsDir).Run()
	}

	return nil
}
