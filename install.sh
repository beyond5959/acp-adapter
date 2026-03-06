#!/bin/sh
# ACP Adapter Installer
# Usage: curl -sSL https://raw.githubusercontent.com/beyond5959/acp-adapter/master/install.sh | sh

set -e

REPO="beyond5959/acp-adapter"
BINARY_NAME="acp-adapter"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Colors for output (only if stdout is a tty)
RED=''
GREEN=''
YELLOW=''
RESET=''

if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    RESET='\033[0m'
fi

info() {
    printf "%b%s%b\n" "$GREEN" "$1" "$RESET"
}

warn() {
    printf "%b%s%b\n" "$YELLOW" "$1" "$RESET"
}

error() {
    printf "%b%s%b\n" "$RED" "$1" "$RESET" >&2
}

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Linux*)     echo "linux";;
        Darwin*)    echo "darwin";;
        CYGWIN*|MINGW*|MSYS*) echo "windows";;
        *)          echo "unknown";;
    esac
}

# Detect architecture
detect_arch() {
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)   echo "x86_64";;
        arm64|aarch64)  echo "aarch64";;
        armv7l|armhf)   echo "armv7";;
        *)              echo "$arch";;
    esac
}

# Get the latest release version
get_latest_version() {
    curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | \
        grep '"tag_name":' | \
        sed -E 's/.*"tag_name": "([^"]+)".*/\1/'
}

# Download file with fallback
download() {
    url="$1"
    output="$2"
    
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$output"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$output"
    else
        error "curl or wget is required"
        exit 1
    fi
}

main() {
    info "🔧 ACP Adapter Installer"
    echo ""
    
    # Detect platform
    OS=$(detect_os)
    ARCH=$(detect_arch)
    
    if [ "$OS" = "unknown" ]; then
        error "Unsupported operating system: $(uname -s)"
        exit 1
    fi
    
    if [ "$OS" = "windows" ]; then
        error "Windows is not supported by this installer. Please download the release manually."
        exit 1
    fi
    
    info "Detected platform: ${OS}/${ARCH}"
    
    # Determine version
    if [ -n "$VERSION" ]; then
        VERSION="${VERSION#v}"
        VERSION="v${VERSION}"
        info "Using specified version: ${VERSION}"
    else
        info "Fetching latest release..."
        VERSION=$(get_latest_version)
        if [ -z "$VERSION" ]; then
            error "Failed to fetch latest version"
            exit 1
        fi
        info "Latest version: ${VERSION}"
    fi
    
    # Construct download URL
    TARGET="${ARCH}-${OS}"
    if [ "$OS" = "windows" ]; then
        EXT="zip"
    else
        EXT="tar.gz"
    fi
    
    FILENAME="${BINARY_NAME}-${VERSION}-${TARGET}.${EXT}"
    URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"
    
    # Create temp directory
    TMP_DIR=$(mktemp -d)
    trap 'rm -rf "$TMP_DIR"' EXIT
    
    info "Downloading ${FILENAME}..."
    download "$URL" "${TMP_DIR}/${FILENAME}"
    
    # Extract
    info "Extracting..."
    cd "$TMP_DIR"
    if [ "$EXT" = "zip" ]; then
        if command -v unzip >/dev/null 2>&1; then
            unzip -q "$FILENAME"
        else
            error "unzip is required to extract the archive"
            exit 1
        fi
    else
        tar -xzf "$FILENAME"
    fi
    
    # Find the binary
    EXTRACT_DIR="${BINARY_NAME}-${VERSION}-${TARGET}"
    if [ "$OS" = "windows" ]; then
        BINARY_PATH="${EXTRACT_DIR}/${BINARY_NAME}.exe"
    else
        BINARY_PATH="${EXTRACT_DIR}/${BINARY_NAME}"
    fi
    
    if [ ! -f "$BINARY_PATH" ]; then
        error "Binary not found in archive: ${BINARY_PATH}"
        ls -la "$EXTRACT_DIR" || true
        exit 1
    fi
    
    # Make executable
    chmod +x "$BINARY_PATH"
    
    # Determine install location
    if [ -w "$INSTALL_DIR" ] || [ "$INSTALL_DIR" != "/usr/local/bin" ]; then
        DEST="${INSTALL_DIR}/${BINARY_NAME}"
    else
        # Try sudo or fallback to ~/.local/bin
        if command -v sudo >/dev/null 2>&1; then
            USE_SUDO=1
            DEST="${INSTALL_DIR}/${BINARY_NAME}"
        else
            warn "No write permission to ${INSTALL_DIR} and sudo not available"
            INSTALL_DIR="${HOME}/.local/bin"
            DEST="${INSTALL_DIR}/${BINARY_NAME}"
            mkdir -p "$INSTALL_DIR"
        fi
    fi
    
    # Install
    info "Installing to ${DEST}..."
    if [ -n "$USE_SUDO" ]; then
        sudo mv "$BINARY_PATH" "$DEST"
    else
        mv "$BINARY_PATH" "$DEST"
    fi
    
    # Verify installation
    if command -v "$BINARY_NAME" >/dev/null 2>&1; then
        INSTALLED_VERSION=$("$BINARY_NAME" --version 2>/dev/null || echo "unknown")
        info "✅ Successfully installed ${BINARY_NAME} ${INSTALLED_VERSION}"
    elif [ -x "$DEST" ]; then
        INSTALLED_VERSION=$("$DEST" --version 2>/dev/null || echo "unknown")
        info "✅ Successfully installed ${BINARY_NAME} ${INSTALLED_VERSION}"
        
        # Check if install dir is in PATH
        case ":${PATH}:" in
            *":${INSTALL_DIR}:"*) ;;
            *)
                warn ""
                warn "⚠️  ${INSTALL_DIR} is not in your PATH"
                warn "   Add the following to your shell profile:"
                warn "   export PATH=\"${INSTALL_DIR}:\$PATH\""
                ;;
        esac
    else
        error "Installation failed"
        exit 1
    fi
    
    echo ""
    info "Usage: ${BINARY_NAME} --adapter codex"
    info "   or: ${BINARY_NAME} --adapter claude"
}

# Run main function
main
