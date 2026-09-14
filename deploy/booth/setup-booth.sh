#!/bin/bash
# setup-booth.sh — install gphoto2, check the camera, and put the agent on the
# machine, on macOS and Linux.
#
# What each platform can and cannot be:
# - macOS: a development machine, not a booth. There is no Assigned Access
#   equivalent and no printer backend, and no macOS build is published, so this
#   path downloads nothing: it needs a bykami checkout and runs the agent with
#   `go run`.
# - Linux: can be a booth except that printing has no backend other than sim,
#   because the spooler is Windows-only. The Linux VPS is provisioned by Ansible
#   and has no USB bus, so it proves nothing about the camera.
#
# This script asks before installing anything, and reads answers from the
# terminal rather than stdin so that `curl ... | bash` still prompts.
#
# It verifies the SHA256 digest of anything it downloads. It does not verify the
# ed25519 signature: the agent verifies every self-update against the public key
# compiled into it, and that is where authentication starts.

set -euo pipefail

REPO="yudhabhaktin/bykami"
ASSET="bykami-agent-linux-amd64"
export ASSET

die() { echo "$*" >&2; exit 1; }

# Prompts come from the terminal, not stdin: under `curl | bash` stdin is the
# script itself, so a plain `read` would eat the script instead of asking.
ask() {
    local prompt="$1" ans
    [[ -r /dev/tty ]] || die "No terminal to ask on. Download the script and run it instead of piping it."
    read -r -p "$prompt [y/N] " ans < /dev/tty
    [[ "$ans" =~ ^[Yy]$ ]]
}

os=$(uname -s)
case "$os" in
    Linux)
        if command -v apt-get >/dev/null 2>&1; then
            PKG="apt-get install -y"
        elif command -v dnf >/dev/null 2>&1; then
            PKG="dnf install -y"
        elif command -v pacman >/dev/null 2>&1; then
            PKG="pacman -S --noconfirm"
        else
            die "No supported package manager found (apt, dnf, pacman)."
        fi
        ;;
    Darwin)
        command -v brew >/dev/null 2>&1 || die "Homebrew is required. Install from https://brew.sh"
        PKG="brew install"
        ;;
    *)
        die "Unsupported platform: $os"
        ;;
esac

echo "[setup-booth] platform: $os"

# 1. gphoto2. No driver work on either platform: that is the Windows step only.
if command -v gphoto2 >/dev/null 2>&1; then
    echo "[setup-booth] gphoto2: already installed ($(gphoto2 --version 2>/dev/null | head -1))"
elif ask "gphoto2 is not installed. Install it now with $PKG?"; then
    if [[ "$os" == "Darwin" ]]; then
        $PKG gphoto2 # homebrew refuses to run under sudo, and does not need to
    else
        sudo $PKG gphoto2
    fi
else
    die "gphoto2 is required. Install it and run this script again."
fi

# 2. Linux: let a non-root user open the camera.
#
# TAG+="uaccess" rather than GROUP="plugdev": it grants the user at the console
# access without needing a group to exist, and plugdev is a Debian-family thing
# that Fedora and Arch do not have — there the rule would be silently inert.
if [[ "$os" == "Linux" ]]; then
    RULE=/etc/udev/rules.d/50-bykami-camera.rules
    if [[ -f "$RULE" ]]; then
        echo "[setup-booth] udev rule already exists: $RULE"
    elif ask "Install a udev rule so the camera is usable without root?"; then
        echo 'SUBSYSTEM=="usb", ATTR{idVendor}=="04a9", TAG+="uaccess"' | sudo tee "$RULE" >/dev/null
        sudo udevadm control --reload-rules
        sudo udevadm trigger
        echo "[setup-booth] wrote $RULE and reloaded udev."
        echo "[setup-booth] Unplug the camera and plug it back in for it to apply."
    fi
fi

# 3. The agent.
#
# A checkout is the macOS path (no darwin build is published) and the Linux one
# when somebody is working on the code; otherwise the released binary.
AGENT_DIR=""
if [[ -d ./agent/cmd/bykami-agent ]]; then
    AGENT_DIR="./agent"
elif [[ -d ./cmd/bykami-agent ]]; then
    AGENT_DIR="."
fi

agent() {
    if [[ -n "$AGENT_DIR" ]]; then
        ( cd "$AGENT_DIR" && go run ./cmd/bykami-agent "$@" )
    else
        "$HOME/.local/bin/bykami-agent" "$@"
    fi
}

if [[ "$os" == "Darwin" ]]; then
    if [[ -n "$AGENT_DIR" ]]; then
        echo "[setup-booth] checkout found at $AGENT_DIR; running the agent from source"
    else
        echo "[setup-booth] No macOS build is published and no checkout was found."
        echo "[setup-booth] Clone the repository and run this script from its root."
    fi
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
    if [[ -x "$INSTALL_DIR/bykami-agent" ]]; then
        echo "[setup-booth] bykami-agent: already installed at $INSTALL_DIR/bykami-agent"
    else
        TAG=$(curl -fsSL --max-time 30 \
            -H "Accept: application/vnd.github+json" \
            "https://api.github.com/repos/$REPO/releases?per_page=100" \
            | python3 -c '
import json, sys, os
want = os.environ["ASSET"]
rs = [r for r in json.load(sys.stdin)
      if r["tag_name"].startswith("agent-") and not r["draft"] and not r["prerelease"]
      and any(a["name"] == want for a in r.get("assets", []))]
rs.sort(key=lambda r: r.get("published_at") or r.get("created_at") or "", reverse=True)
if rs:
    print(rs[0]["tag_name"])
' 2>/dev/null) || die "Could not read the releases list from GitHub."
        [[ -n "$TAG" ]] || die "No agent-* release carrying $ASSET was found."

        TMP=$(mktemp -d)
        trap 'rm -rf "$TMP"' EXIT
        BASE="https://github.com/$REPO/releases/download/$TAG"
        echo "[setup-booth] downloading $TAG ..."
        curl -fsSL --max-time 180 -o "$TMP/$ASSET" "$BASE/$ASSET"
        curl -fsSL --max-time 30 -o "$TMP/$ASSET.sha256" "$BASE/$ASSET.sha256"
        ( cd "$TMP" && sha256sum -c "$ASSET.sha256" >/dev/null ) || die "SHA256 mismatch — the download is not what was published."
        install -m 755 "$TMP/$ASSET" "$INSTALL_DIR/bykami-agent"
        echo "[setup-booth] installed $INSTALL_DIR/bykami-agent ($TAG)"
        case ":$PATH:" in
            *":$INSTALL_DIR:"*) ;;
            *) echo "[setup-booth] NOTE: $INSTALL_DIR is not on your PATH." ;;
        esac
    fi
fi

# 4. The doctor answers "is the camera there?", so run it now rather than
# leaving it to be discovered later. It is given the same root that step 5
# prints, so the two commands it shows do not disagree about where the booth
# lives.
BOOTH_ROOT="$HOME/bykami-booth"
HOT="$BOOTH_ROOT/hot"
mkdir -p "$HOT"

echo "[setup-booth] ---"
# Note the order: the flags come before the subcommand, because the flag
# package stops at the first non-flag argument. `doctor -root X` would run the
# doctor on the default root and quietly ignore X.
agent -root "$BOOTH_ROOT" doctor || true
echo "[setup-booth] ---"

# 5. The command to start a booth, with the paths this machine just got.

flags=(-root "$BOOTH_ROOT" -source hotfolder -hot-folder "$HOT" -camera-tool gphoto2
       -printer sim -update-repo "$REPO")

echo
echo "Start the booth:"
if [[ -n "$AGENT_DIR" ]]; then
    echo "  ( cd $AGENT_DIR && go run ./cmd/bykami-agent ${flags[*]} )"
else
    echo "  $HOME/.local/bin/bykami-agent ${flags[*]}"
fi
echo
echo "Printing is simulated here; the DNP printer backend is Windows-only."
echo "Updates are on: the agent polls $REPO and verifies every release against"
echo "the public key compiled into it."
