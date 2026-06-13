#!/usr/bin/env bash
# setup.sh — Install or upgrade Kyvik from a release archive (or source build).
#
# Usage:
#   sudo ./setup.sh              # Fresh install
#   sudo ./setup.sh --upgrade    # Upgrade (preserves config)
#
# Expects to be run from the archive/repo root with this layout:
#   bin/kyvik, bin/kyvik-sandbox, configs/, deploy/
set -euo pipefail

UPGRADE=false
for arg in "$@"; do
    case "$arg" in
        --upgrade) UPGRADE=true ;;
        -h|--help)
            echo "Usage: sudo $0 [--upgrade]"
            echo ""
            echo "  --upgrade   Update binaries and templates without touching config"
            exit 0
            ;;
        *)
            echo "Unknown option: $arg" >&2
            echo "Usage: sudo $0 [--upgrade]" >&2
            exit 1
            ;;
    esac
done

# --- Resolve paths relative to this script ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# When run from archive root, setup.sh is at the top level and bin/ is a sibling.
# When run from the repo, setup.sh is in deploy/ and bin/ is at ../bin or built to build/.
if [ -d "$SCRIPT_DIR/bin" ]; then
    # Archive layout: setup.sh sits next to bin/, configs/, deploy/
    ROOT="$SCRIPT_DIR"
elif [ -d "$SCRIPT_DIR/../bin" ]; then
    # Repo layout: deploy/setup.sh, bin/ is sibling of deploy/
    ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
else
    echo "Error: cannot find bin/ directory relative to $SCRIPT_DIR" >&2
    exit 1
fi

BINARY="$ROOT/bin/kyvik"
SANDBOX_BINARY="$ROOT/bin/kyvik-sandbox"
CONFIGS_DIR="$ROOT/configs"
DEPLOY_DIR="$ROOT/deploy"

# If invoked from Makefile after build, binaries might be in build/ instead.
if [ ! -f "$BINARY" ] && [ -f "$ROOT/build/kyvik" ]; then
    BINARY="$ROOT/build/kyvik"
    SANDBOX_BINARY="$ROOT/build/kyvik-sandbox"
fi

# --- Preflight checks ---
if [ "$(id -u)" -ne 0 ]; then
    echo "Error: must run as root (sudo $0)" >&2
    exit 1
fi

if [ ! -f "$BINARY" ]; then
    echo "Error: kyvik binary not found at $BINARY" >&2
    exit 1
fi

if [ ! -f "$SANDBOX_BINARY" ]; then
    echo "Error: kyvik-sandbox binary not found at $SANDBOX_BINARY" >&2
    exit 1
fi

# --- 1. Create system user ---
if ! id -u kyvik >/dev/null 2>&1; then
    echo "Creating system user: kyvik"
    useradd --system --shell /usr/sbin/nologin --home-dir /opt/kyvik kyvik
else
    echo "System user kyvik already exists"
fi

# --- 2. Create directory structure ---
echo "Creating directory structure..."
mkdir -p /opt/kyvik/bin /opt/kyvik/configs/templates /etc/kyvik /var/lib/kyvik /var/log/kyvik

# --- 3. Copy binaries ---
echo "Installing binaries to /opt/kyvik/bin/"
cp "$BINARY" /opt/kyvik/bin/kyvik
cp "$SANDBOX_BINARY" /opt/kyvik/bin/kyvik-sandbox
chmod 755 /opt/kyvik/bin/kyvik /opt/kyvik/bin/kyvik-sandbox

# --- 4. Copy permission templates (always overwrite) ---
if [ -d "$CONFIGS_DIR/templates" ]; then
    echo "Installing permission templates..."
    cp "$CONFIGS_DIR/templates/"*.yaml /opt/kyvik/configs/templates/
fi

# --- 5. Copy default config (skip if exists or upgrading) ---
if [ "$UPGRADE" = true ]; then
    echo "Upgrade mode: preserving /etc/kyvik/kyvik.yaml"
elif [ -f /etc/kyvik/kyvik.yaml ]; then
    echo "Config already exists at /etc/kyvik/kyvik.yaml — skipping"
else
    if [ -f "$CONFIGS_DIR/kyvik.example.yaml" ]; then
        cp "$CONFIGS_DIR/kyvik.example.yaml" /etc/kyvik/kyvik.yaml
        echo "Installed default config to /etc/kyvik/kyvik.yaml"
    else
        echo "Warning: no example config found, skipping config install"
    fi
fi

# --- 6. Create kv symlink ---
ln -sf /opt/kyvik/bin/kyvik /usr/local/bin/kv
echo "Created symlink: /usr/local/bin/kv -> /opt/kyvik/bin/kyvik"

# --- 7. Set ownership ---
chown -R kyvik:kyvik /var/lib/kyvik /var/log/kyvik
chown -R root:kyvik /etc/kyvik /opt/kyvik
chmod 750 /etc/kyvik

# --- 8. Install/regenerate systemd service ---
GENERATE_SCRIPT="$DEPLOY_DIR/systemd/generate-service.sh"
if [ -f "$GENERATE_SCRIPT" ] && [ -f /etc/kyvik/kyvik.yaml ]; then
    echo "Generating systemd service..."
    bash "$GENERATE_SCRIPT"
    systemctl daemon-reload
    systemctl enable kyvik.service
    systemctl restart kyvik.service
    echo "Service kyvik.service enabled and started"
elif [ -f /etc/systemd/system/kyvik.service ]; then
    # Service file exists but no generator — just restart.
    systemctl daemon-reload
    systemctl restart kyvik.service
    echo "Service restarted"
else
    echo "Skipping systemd setup (no config file yet)"
fi

# --- Summary ---
VERSION=$(/opt/kyvik/bin/kyvik --version 2>/dev/null || echo "unknown")
echo ""
echo "=== Kyvik ${VERSION} installed ==="
echo "  Binaries:  /opt/kyvik/bin/"
echo "  Config:    /etc/kyvik/kyvik.yaml"
echo "  Data:      /var/lib/kyvik/"
echo "  Logs:      journalctl -u kyvik"
echo "  Shortcut:  kv (symlink to kyvik)"

if [ "$UPGRADE" = false ] && [ ! -f /etc/kyvik/env ]; then
    echo ""
    echo "Next steps:"
    echo "  1. Edit /etc/kyvik/kyvik.yaml"
    echo "  2. Generate a master key:  openssl rand -base64 32 > /etc/kyvik/env"
    echo "  3. Add API keys to /etc/kyvik/env"
    echo "  4. Regenerate systemd:     bash $DEPLOY_DIR/systemd/generate-service.sh"
fi
