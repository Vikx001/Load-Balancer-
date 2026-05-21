# Omega-LB Desktop v1.0.0-alpha.1 Installation Guide

## Platform: macOS (ARM64 / Apple Silicon)

### Download
- **File**: `OmegaLBDesktop-1.0.0-alpha.1-macos-arm64.zip` (225 MB)
- **Supported**: macOS 12.0 Monterey and later
- **Architecture**: Apple Silicon M1, M2, M3, M4 (ARM64 only)

### Installation Steps

#### Method 1: Standard Installation (Recommended)

1. **Download** the `.zip` file from this release
2. **Unzip** to any location (e.g., Downloads folder)
3. **Open** Finder, navigate to the extracted folder
4. **Right-click** on `OmegaLBDesktop-macos-arm64.app`
5. **Select** "Open" (this bypasses Gatekeeper on first launch)
6. **Grant permissions** when prompted by macOS
7. **Application launches** - ready to use

#### Method 2: Move to Applications Folder (Recommended for Regular Use)

1. **Unzip** the downloaded file
2. **Open** Finder, go to **Applications** folder (Cmd+Shift+A)
3. **Drag and drop** `OmegaLBDesktop-macos-arm64.app` into Applications folder
4. **Launch** from Applications:
   - Open Applications folder
   - Double-click `OmegaLBDesktop-macos-arm64.app`
   - Approve Gatekeeper prompt on first run

#### Method 3: Command Line Installation

```bash
# Unzip
unzip OmegaLBDesktop-1.0.0-alpha.1-macos-arm64.zip

# Move to Applications (optional but recommended)
mv OmegaLBDesktop-macos-arm64.app /Applications/OmegaLBDesktop.app

# Remove quarantine flag (if needed)
xattr -d com.apple.quarantine /Applications/OmegaLBDesktop.app

# Launch
open /Applications/OmegaLBDesktop.app
```

### Troubleshooting

#### Issue: "OmegaLBDesktop cannot be opened because the developer cannot be verified"

**Solution**:
1. Open **System Settings** → **Privacy & Security**
2. Scroll down to "Security" section
3. Find the message about OmegaLBDesktop
4. Click **"Open Anyway"**
5. Enter your Mac password if prompted

**Alternative**: Use the command-line bypass:
```bash
xattr -d com.apple.quarantine ~/Downloads/OmegaLBDesktop-macos-arm64.app
```

#### Issue: "Permission denied" when launching

**Solution**: Grant execute permissions:
```bash
chmod +x OmegaLBDesktop-macos-arm64.app/Contents/MacOS/OmegaLBDesktop
```

#### Issue: Application crashes on startup

**Solution**: Check system logs:
```bash
log stream --predicate 'process == "OmegaLBDesktop"' --level debug
```

Check that required ports are available:
```bash
lsof -i :8080  # Proxy port
lsof -i :8501  # Dashboard port (Streamlit)
```

If ports are in use, modify `omega-lb.yaml` before starting.

#### Issue: "No module named 'demo'" or similar import errors

**Solution**: Ensure you're running the version from this release (v1.0.0-alpha.1 or later). Module bundling is included in the packaged app.

### System Requirements

| Component | Requirement |
|-----------|-------------|
| **OS** | macOS 12+ (Monterey or later) |
| **Processor** | Apple Silicon (M1/M2/M3/M4) or Intel x64 with Rosetta 2 |
| **RAM** | 4 GB minimum, 8 GB recommended |
| **Disk** | 500 MB free space |
| **Network** | Localhost access (127.0.0.1) |

### Uninstallation

#### Method 1: Finder
1. Open **Finder** → **Applications**
2. Find `OmegaLBDesktop` or `OmegaLBDesktop-macos-arm64.app`
3. **Right-click** → **Move to Trash**
4. **Empty Trash**

#### Method 2: Command Line
```bash
rm -rf /Applications/OmegaLBDesktop.app
# or if installed from this release folder:
rm -rf ~/Downloads/OmegaLBDesktop-macos-arm64.app
```

#### Clean Cached Config
```bash
rm ~/.omega-lb.yaml  # Remove cached config (if any)
```

---

## Platform: Windows (x64 / Intel/AMD 64-bit)

### Download
- **File**: `OmegaLBDesktop-1.0.0-alpha.1-win-x64.exe` (executable) or `.msi` (installer)
- **Supported**: Windows 10 Build 19041+, Windows 11
- **Architecture**: x86-64 only (64-bit Windows)

### Installation Steps

#### Method 1: MSI Installer (Recommended)

1. **Download** `OmegaLBDesktop-1.0.0-alpha.1-win-x64.msi`
2. **Double-click** the `.msi` file
3. **Follow installer wizard**:
   - Accept license terms
   - Choose installation directory (default: `C:\Program Files\OmegaLBDesktop`)
   - Click **Install**
4. **Finish** - application installed
5. **Launch**: Start Menu → type "OmegaLB" → Enter

#### Method 2: Portable Executable (No Installation)

1. **Download** `OmegaLBDesktop-1.0.0-alpha.1-win-x64-portable.exe`
2. **Place** in desired directory (e.g., `C:\Tools\OmegaLBDesktop\`)
3. **Double-click** to launch (no installation needed)
4. **Config/logs** stored in same directory as executable

### Troubleshooting (Windows)

#### Issue: "The app can't open - Microsoft protected your PC"

**Solution**: Allow the app to run:
1. Click **More info**
2. Click **Run anyway**
3. Approve User Account Control (UAC) prompt

#### Issue: Port 8080 or 8501 already in use

**Solution**: Modify `omega-lb.yaml`:
```yaml
proxy:
  port: 8080  # Change to 9080, 7080, etc.
dashboard:
  port: 8501  # Change to 9501, 7501, etc.
```

#### Issue: "Path contains spaces" error

**Solution**: Install to a path without spaces:
- Good: `C:\OmegaLB`, `C:\Tools\LoadBalancer`
- Avoid: `C:\Program Files\My App\OmegaLB` (contains spaces)

#### Issue: Windows Defender/Antivirus flagging executable

**Solution**:
1. This is a false positive (common for PyInstaller-packaged apps)
2. Add exception to your antivirus software
3. Or report to vendor: `OmegaLBDesktop.exe` is safe

### System Requirements (Windows)

| Component | Requirement |
|-----------|-------------|
| **OS** | Windows 10 Build 19041+ or Windows 11 |
| **Processor** | x86-64 (64-bit only) |
| **RAM** | 4 GB minimum, 8 GB recommended |
| **Disk** | 500 MB free space |
| **Runtime** | .NET Framework 4.7+ (usually pre-installed) |
| **Visual C++** | 2015 Redistributable or later |

### Uninstallation (Windows)

#### Method 1: Control Panel
1. Open **Control Panel** → **Programs and Features**
2. Find **OmegaLBDesktop**
3. Click **Uninstall**
4. Follow uninstaller wizard

#### Method 2: Settings App
1. Open **Settings** → **Apps** → **Apps & Features**
2. Search for **OmegaLBDesktop**
3. Click **Uninstall**

#### Method 3: Portable Executable
Simply delete the `.exe` file. No registry entries or cleanup needed.

---

## Verify Installation

After installation on either platform, verify it's working:

```bash
# macOS
open /Applications/OmegaLBDesktop.app

# Windows (from Command Prompt)
C:\Program Files\OmegaLBDesktop\OmegaLBDesktop.exe
```

**Expected**: Application window launches showing:
- Title bar: "Omega-LB Desktop v1.0.0-alpha.1"
- Backend Wiring panel
- Start Stack button (initially disabled until backends configured)
- Activity log showing initialization messages

---

## Next Steps

1. **Configure Backends**:
   - Click "Backend Wiring" panel
   - Either: Click "Load Demo Targets" for localhost backends
   - Or: Add your real backend host/port manually

2. **Test Connectivity**:
   - Click "Test All Backends" button
   - Verify all backends show "UP" status (green)

3. **Start Stack**:
   - Click "Start Stack" button
   - Wait for all KPI tiles to show live data (Tick, RPS, etc.)

4. **View Dashboard**:
   - Click "Open Dashboard" button
   - Streamlit visualization opens in browser

5. **Stop**:
   - Click "Stop Stack" to gracefully shutdown all processes

---

## Support

- **GitHub Issues**: [Report bugs](https://github.com/Vikx001/Load-Balancer-/issues)
- **Documentation**: See [README.md](../../README.md)
- **Release Notes**: [RELEASE_NOTES.md](../../RELEASE_NOTES.md)

---

**Thank you for using Omega-LB Desktop v1.0.0-alpha.1!**
