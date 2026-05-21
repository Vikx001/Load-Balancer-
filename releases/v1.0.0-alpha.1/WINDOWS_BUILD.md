# Windows Build Instructions for Omega-LB Desktop v1.0.0-alpha.1

## Prerequisites

### Required Software

- **Windows 10/11** (x86-64 architecture)
- **Python 3.13+** from [python.org](https://www.python.org/downloads/)
  - During installation, **MUST** check "Add Python to PATH"
- **Git for Windows** from [git-scm.com](https://git-scm.com/download/win)
- **PowerShell 5.0+** (included in Windows 10/11)
- **Visual C++ Build Tools**:
  - Download from [Microsoft](https://visualstudio.microsoft.com/visual-cpp-build-tools/)
  - Or install **Visual Studio Community** with C++ workload

### System Requirements

- Minimum 4 GB RAM (8 GB recommended for PyInstaller build)
- 2 GB free disk space (for build artifacts, dependencies)
- Administrator access (for Visual C++ installation)

---

## Build Steps

### 1. Clone Repository

```powershell
# Open PowerShell as Administrator
cd C:\Projects  # Or your preferred location

# Clone the repository
git clone https://github.com/Vikx001/Load-Balancer-.git
cd "Load-Balancer-"
```

### 2. Verify Python Installation

```powershell
python --version
pip --version
```

Expected output:
```
Python 3.13.x
pip 24.x.x
```

### 3. Create Virtual Environment (Desktop)

```powershell
# Navigate to project root
cd C:\Projects\Load-Balancer-

# Create virtual environment for desktop build
python -m venv .venv-desktop

# Activate virtual environment
.\.venv-desktop\Scripts\Activate.ps1
```

If you get an "execution policy" error:
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```

### 4. Install Dependencies

```powershell
# Install PyInstaller and desktop dependencies
pip install -q -r requirements.txt -r desktop/requirements.txt

# Verify installations
pip list | findstr PySide6
pip list | findstr PyInstaller
```

### 5. Run Build Script

```powershell
# Ensure you're in the project root directory
cd C:\Projects\Load-Balancer-

# Run the Windows build script
.\desktop\build_windows.ps1
```

Expected output:
```
Built executable: dist\OmegaLBDesktop\OmegaLBDesktop.exe
```

### 6. Verify Build Output

```powershell
# Check if executable exists
ls dist\OmegaLBDesktop\OmegaLBDesktop.exe

# Check size (should be 150-200 MB)
(Get-Item dist\OmegaLBDesktop\OmegaLBDesktop.exe).Length / 1MB
```

---

## Create Distribution Packages

### Option A: Create Portable ZIP

```powershell
# Navigate to project root
cd C:\Projects\Load-Balancer-

# Create release directory
mkdir "releases\v1.0.0-alpha.1"

# Copy the dist folder to releases
Copy-Item -Recurse "dist\OmegaLBDesktop" "releases\v1.0.0-alpha.1\OmegaLBDesktop-win-x64"

# Create ZIP archive (requires 7-Zip or use built-in)
# Using PowerShell's Compress-Archive:
Compress-Archive -Path "releases\v1.0.0-alpha.1\OmegaLBDesktop-win-x64" `
                 -DestinationPath "releases\v1.0.0-alpha.1\OmegaLBDesktop-1.0.0-alpha.1-win-x64.zip"

# Verify
ls "releases\v1.0.0-alpha.1\*.zip"
```

### Option B: Create Portable EXE

```powershell
# Create standalone executable
Copy-Item "dist\OmegaLBDesktop\OmegaLBDesktop.exe" `
          "releases\v1.0.0-alpha.1\OmegaLBDesktop-1.0.0-alpha.1-win-x64-portable.exe"
```

### Option C: Create MSI Installer

For professional distribution, use **WiX Toolset** (requires additional setup):

1. Download WiX Toolset from [wixtoolset.org](https://wixtoolset.org/)
2. Create `.wxs` configuration file
3. Compile with `candle.exe` and `light.exe`

**Alternative**: Use **Inno Setup** (easier):
1. Download from [jrsoftware.org](https://jrsoftware.org/isdl.php)
2. Create installer script
3. Compile installer

(For this release, MSI creation is optional; portable EXE is sufficient)

---

## Troubleshooting Build Issues

### Issue: "Python command not found"

**Solution**: Ensure Python is in PATH:
```powershell
# Check if Python is accessible
python -c "print('Python is installed')"

# If not, manually add to PATH (restart PowerShell after):
$env:Path += ";C:\Python313"
```

### Issue: "Cannot find pip"

**Solution**: Use Python's module:
```powershell
python -m pip install --upgrade pip
```

### Issue: "Virtual environment activation fails"

**Solution**: Check execution policy:
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
.\.venv-desktop\Scripts\Activate.ps1
```

### Issue: "PyInstaller module not found"

**Solution**: Ensure virtual environment is activated:
```powershell
.\.venv-desktop\Scripts\Activate.ps1
pip install PyInstaller
```

### Issue: "Build fails with 'hidden-import' errors"

**Solution**: Verify all dependencies are installed:
```powershell
pip install demo.backends demo.proxy ml.kan ml.cbf
```

Or manually add hidden imports to `build_windows.ps1`:
```powershell
--hidden-import aiohttp `
--hidden-import streamlit `
--hidden-import PySide6 `
```

### Issue: "ModuleNotFoundError: No module named 'demo'"

**Solution**: This is expected during build; PyInstaller will bundle it. Ensure `demo/` and `ml/` directories exist at project root.

---

## Testing the Built Executable

```powershell
# Navigate to build output
cd .\dist\OmegaLBDesktop

# Launch the application
.\OmegaLBDesktop.exe

# Expected: Native Windows application window opens
# Title: "Omega-LB Desktop v1.0.0-alpha.1"
# Shows: Backend Wiring panel, Start Stack button, etc.
```

---

## Distributing Your Build

1. **Compress** the `dist\OmegaLBDesktop` folder to `.zip`
2. **Upload** to GitHub Releases or your distribution platform
3. **Document** system requirements and installation steps
4. **Sign** the executable (optional but recommended for Windows):

```powershell
# If you have a code signing certificate:
# SignTool sign /f MyCertificate.pfx /p password /t http://timestamp.server.com `
#                /d "Omega-LB Desktop" OmegaLBDesktop.exe
```

---

## Build Automation (GitHub Actions)

For automated builds, create `.github/workflows/build-windows.yml`:

```yaml
name: Build Windows Executable

on:
  push:
    tags:
      - 'v*'

jobs:
  build:
    runs-on: windows-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-python@v4
        with:
          python-version: '3.13'
      - run: pip install -r requirements.txt -r desktop/requirements.txt
      - run: .\desktop\build_windows.ps1
      - uses: actions/upload-artifact@v3
        with:
          name: OmegaLBDesktop-win-x64
          path: dist/OmegaLBDesktop/
```

---

## Next Steps After Build

1. **Test** the executable on a clean Windows machine
2. **Create** installation guide (see [INSTALLATION.md](INSTALLATION.md))
3. **Release** on GitHub with version tag: `v1.0.0-alpha.1`
4. **Announce** in release notes with checksums (SHA256):

```powershell
certutil -hashfile OmegaLBDesktop-1.0.0-alpha.1-win-x64.exe SHA256
```

---

## Version Numbering

All Windows builds for this release must use:
- **Version**: `1.0.0-alpha.1`
- **Filename**: `OmegaLBDesktop-1.0.0-alpha.1-win-x64{-portable}.exe` or `.zip`
- **Internal version**: Match `desktop/omegalb_desktop.py` line: `__VERSION__ = "1.0.0-alpha.1"`

---

## Support

- **Build issues**: [GitHub Issues](https://github.com/Vikx001/Load-Balancer-/issues)
- **Windows-specific problems**: Note OS build number and Python version in issue
- **Documentation**: [INSTALLATION.md](INSTALLATION.md)

---

**Complete Windows build instructions for v1.0.0-alpha.1**
