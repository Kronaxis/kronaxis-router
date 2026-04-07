#!/bin/sh
# Kronaxis Router installer
# Usage: curl -fsSL https://raw.githubusercontent.com/Kronaxis/kronaxis-router/main/install.sh | sh
set -e

REPO="Kronaxis/kronaxis-router"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY="kronaxis-router"

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
        *) echo "unsupported"; return 1 ;;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "unsupported"; return 1 ;;
    esac
}

# Get latest version from GitHub
get_latest_version() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/'
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/'
    else
        echo "Error: curl or wget required" >&2
        exit 1
    fi
}

# Download and install
install() {
    OS=$(detect_os)
    ARCH=$(detect_arch)

    if [ "$OS" = "unsupported" ] || [ "$ARCH" = "unsupported" ]; then
        echo "Error: unsupported platform $(uname -s)/$(uname -m)"
        exit 1
    fi

    # Allow version override
    VERSION="${VERSION:-$(get_latest_version)}"
    if [ -z "$VERSION" ]; then
        echo "Error: could not determine latest version"
        echo "Set VERSION=x.y.z to install a specific version"
        exit 1
    fi

    EXT="tar.gz"
    if [ "$OS" = "windows" ]; then
        EXT="zip"
        BINARY="${BINARY}.exe"
    fi

    FILENAME="${BINARY}_${VERSION}_${OS}_${ARCH}.${EXT}"
    URL="https://github.com/${REPO}/releases/download/v${VERSION}/${FILENAME}"
    CHECKSUM_URL="https://github.com/${REPO}/releases/download/v${VERSION}/checksums.txt"

    echo "Installing kronaxis-router v${VERSION} (${OS}/${ARCH})"

    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT

    echo "Downloading ${URL}"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "${TMPDIR}/${FILENAME}" "$URL"
        curl -fsSL -o "${TMPDIR}/checksums.txt" "$CHECKSUM_URL" 2>/dev/null || true
    else
        wget -q -O "${TMPDIR}/${FILENAME}" "$URL"
        wget -q -O "${TMPDIR}/checksums.txt" "$CHECKSUM_URL" 2>/dev/null || true
    fi

    # Verify checksum if available
    if [ -f "${TMPDIR}/checksums.txt" ]; then
        EXPECTED=$(grep "${FILENAME}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
        if [ -n "$EXPECTED" ]; then
            if command -v sha256sum >/dev/null 2>&1; then
                ACTUAL=$(sha256sum "${TMPDIR}/${FILENAME}" | awk '{print $1}')
            elif command -v shasum >/dev/null 2>&1; then
                ACTUAL=$(shasum -a 256 "${TMPDIR}/${FILENAME}" | awk '{print $1}')
            fi
            if [ -n "$ACTUAL" ] && [ "$EXPECTED" != "$ACTUAL" ]; then
                echo "Error: checksum mismatch"
                echo "  expected: ${EXPECTED}"
                echo "  actual:   ${ACTUAL}"
                exit 1
            fi
            echo "Checksum verified"
        fi
    fi

    # Extract
    echo "Extracting"
    cd "$TMPDIR"
    if [ "$EXT" = "tar.gz" ]; then
        tar xzf "$FILENAME"
    else
        unzip -q "$FILENAME"
    fi

    # Install
    if [ -w "$INSTALL_DIR" ]; then
        cp "$BINARY" "${INSTALL_DIR}/${BINARY}"
        chmod +x "${INSTALL_DIR}/${BINARY}"
    else
        echo "Installing to ${INSTALL_DIR} (requires sudo)"
        sudo cp "$BINARY" "${INSTALL_DIR}/${BINARY}"
        sudo chmod +x "${INSTALL_DIR}/${BINARY}"
    fi

    echo ""
    echo "Installed kronaxis-router v${VERSION} to ${INSTALL_DIR}/${BINARY}"
    echo ""

    # Quick start
    echo "Quick start:"
    echo "  kronaxis-router init         # auto-detect backends, generate config"
    echo "  kronaxis-router              # start the router"
    echo "  kronaxis-router mcp          # start as MCP server (Claude Code integration)"
    echo ""
    echo "Integration:"
    echo "  kronaxis-router init --claude   # configure Claude Code MCP"
    echo "  kronaxis-router init --aider    # configure Aider"
    echo "  kronaxis-router init --cursor   # configure Cursor"
    echo ""
}

install
