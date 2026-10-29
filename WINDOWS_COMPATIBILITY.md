# Windows Compatibility Guide & Architecture

This document outlines the compatibility status, deployment procedures, and engineering requirements for running **Antigravity Account Switcher** natively on Microsoft Windows.

---

## 1. Executive Summary & Compatibility Status

The application in its current state is **fully compatible and deployable on Windows**.

| Component | Status | Mechanism / Notes |
| :--- | :---: | :--- |
| **Go Compilation & Binaries** |  Supported | Compiles cleanly to `windows/amd64` and `windows/arm64` `.exe` without CGO. |
| **Persistence (SQLite)** |  Supported | Uses pure-Go `modernc.org/sqlite`; no GCC, MinGW, or external DLLs required. |
| **Proxy & Failover Engine** |  Supported | Standard Go `net/http` reverse proxy with 100MB body buffer and dynamic Bearer injection. |
| **Web Dashboard** |  Supported | Embedded static assets (`embed.FS`) served over `http://127.0.0.1:<port>`. |
| **OAuth Browser Flow** |  Supported | Explicitly implemented via `rundll32 url.dll,FileProtocolHandler <url>`. |
| **OAuth Auto-Import** |  Supported | Inspects `%USERPROFILE%\.gemini\antigravity-acp` and `antigravity-cli`. |
| **Process Lifecycle** |  Supported | Uses `internal/launcher/lifecycle_other.go` safe stub for non-Linux platforms. |
| **Quota Polling** |  Supported | Transparent fallback: when `/proc` is unavailable, queries Google Cloud Code PA directly. |

---

## 2. Deploying & Running on Windows Today

### Step 1: Build the Binary
From Windows (PowerShell or Command Prompt):
```powershell
go build -o antigravity-account-switcher.exe ./cmd/antigravity-account-switcher
```
*(Or cross-compile from macOS/Linux: `GOOS=windows GOARCH=amd64 go build -o antigravity-account-switcher.exe ./cmd/antigravity-account-switcher`)*

### Step 2: Configure Antigravity Executable Path (One-Time)
If `Antigravity.exe` or `agy` is not already in your Windows `PATH`:
```cmd
antigravity-account-switcher.exe config set antigravity_bin "C:\Users\<YourUser>\AppData\Local\Programs\Antigravity\Antigravity.exe"
```

### Step 3: Run the Application
To launch Antigravity supervised with account rotation:
```cmd
antigravity-account-switcher.exe launch
```
To run as a background/standalone proxy service:
```cmd
antigravity-account-switcher.exe serve
```

---

## 3. Engineering Requirements for 100% Zero-Config Native Windows Parity

While the app runs cleanly today, the following items outline the engineering requirements to achieve zero-config native parity on Windows:

### 3.1. Automatic Executable Probing on Windows
- **Current Behavior**: `internal/config/config.go` (`CandidateAntigravityPaths`) probes Linux and macOS directories. Additionally, line 331 checks `fi.Mode()&0o111 != 0` (POSIX executable bit), which always evaluates to `false` on Windows because `os.Stat` on Windows does not set POSIX mode bits.
- **Requirement**:
  1. Add Windows candidate locations using `%LOCALAPPDATA%` and `%ProgramFiles%`:
     - `%LOCALAPPDATA%\Programs\antigravity\Antigravity.exe`
     - `%LOCALAPPDATA%\Programs\Antigravity\Antigravity.exe`
     - `%ProgramFiles%\Antigravity\Antigravity.exe`
     - `%ProgramFiles(x86)%\Antigravity\Antigravity.exe`
  2. Guard the executable mode check: On `runtime.GOOS == "windows"`, verify that the file exists, is not a directory, and has an executable extension (`.exe` or `.cmd`).

### 3.2. Native Windows Shortcut Creation
- **Current Behavior**: `internal/launcher/desktop.go` installs Linux XDG `.desktop` files in `~/.local/share/applications/`.
- **Requirement**:
  - Implement Windows shortcut creation (`.lnk`) in `%APPDATA%\Microsoft\Windows\Start Menu\Programs\` or the Desktop using PowerShell `WScript.Shell` or COM APIs when `runtime.GOOS == "windows"`.

### 3.3. Path Separator Hygiene in Fallback Helpers
- **Current Behavior**: `internal/launcher/wrap.go` (`defaultDBPath`) constructs default database paths with `fmt.Sprintf("%s/.config/...")`.
- **Requirement**:
  - Replace the private helper with `config.DefaultDBPath()` which uses `filepath.Join()`.

### 3.4. Windows Helper Scripts
- **Current Behavior**: Scripts in `scripts/` (`launch-antigravity.sh`, `install-desktop.sh`) are Bash scripts.
- **Requirement**:
  - Provide PowerShell counterparts (e.g. `scripts/launch-antigravity.ps1`).
