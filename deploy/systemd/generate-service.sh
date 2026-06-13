#!/usr/bin/env bash
# generate-service.sh — Reads kyvik.yaml and generates the appropriate systemd service file.
# Usage: ./deploy/systemd/generate-service.sh [config-path] [output-path]
#
# Defaults:
#   config-path: /etc/kyvik/kyvik.yaml
#   output-path: /etc/systemd/system/kyvik.service
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${1:-/etc/kyvik/kyvik.yaml}"
OUTPUT="${2:-/etc/systemd/system/kyvik.service}"

if [ ! -f "$CONFIG" ]; then
    echo "Error: config file not found: $CONFIG" >&2
    exit 1
fi

# Parse host_access from YAML (simple grep — avoids yq dependency).
HOST_ACCESS=$(grep -E '^\s*host_access:' "$CONFIG" 2>/dev/null | head -1 | sed 's/.*host_access:\s*["'\'']*\([^"'\'']*\)["'\'']*\s*/\1/' | tr -d '[:space:]' || true)
if [ -z "$HOST_ACCESS" ]; then
    HOST_ACCESS="sandbox"
fi

echo "Host access mode: $HOST_ACCESS"

# Select template based on mode.
if [ "$HOST_ACCESS" = "host" ]; then
    TEMPLATE="$SCRIPT_DIR/kyvik-host.service"
else
    TEMPLATE="$SCRIPT_DIR/kyvik-sandbox.service"
fi

if [ ! -f "$TEMPLATE" ]; then
    echo "Error: template not found: $TEMPLATE" >&2
    exit 1
fi

# Parse extra_paths from YAML.
EXTRA_RW=""
EXTRA_RO=""
in_extra_paths=false
in_read_write=false
in_read_only=false

while IFS= read -r line; do
    # Detect extra_paths block.
    if echo "$line" | grep -qE '^\s*extra_paths:'; then
        in_extra_paths=true
        in_read_write=false
        in_read_only=false
        continue
    fi

    # Exit extra_paths block on non-indented line.
    if $in_extra_paths && echo "$line" | grep -qE '^[^ ]'; then
        in_extra_paths=false
        in_read_write=false
        in_read_only=false
        continue
    fi

    if $in_extra_paths; then
        if echo "$line" | grep -qE '^\s*read_write:'; then
            in_read_write=true
            in_read_only=false
            continue
        fi
        if echo "$line" | grep -qE '^\s*read_only:'; then
            in_read_only=true
            in_read_write=false
            continue
        fi

        # Parse list items (- /path/to/dir).
        path=$(echo "$line" | sed -n 's/^\s*-\s*\(.*\)/\1/p' | tr -d '"' | tr -d "'" | xargs)
        if [ -n "$path" ]; then
            if $in_read_write; then
                EXTRA_RW="$EXTRA_RW $path"
            elif $in_read_only; then
                EXTRA_RO="$EXTRA_RO $path"
            fi
        fi
    fi
done < "$CONFIG"

# Trim leading whitespace.
EXTRA_RW=$(echo "$EXTRA_RW" | xargs)
EXTRA_RO=$(echo "$EXTRA_RO" | xargs)

echo "Extra RW paths: ${EXTRA_RW:-<none>}"
echo "Extra RO paths: ${EXTRA_RO:-<none>}"

# Generate service file by replacing placeholders.
sed -e "s|@@EXTRA_RW@@|$EXTRA_RW|g" \
    -e "s|@@EXTRA_RO@@|$EXTRA_RO|g" \
    "$TEMPLATE" > "$OUTPUT"

echo "Service file written to: $OUTPUT"
echo "Run: systemctl daemon-reload && systemctl restart kyvik"
