#!/bin/sh

set -e

info()  { printf "\033[1;34m[INFO]\033[0m %s\n" "$*"; }
success() { printf "\033[1;32m[SUCCESS]\033[0m %s\n" "$*"; }
warn()  { printf "\033[1;33m[WARN]\033[0m %s\n" "$*" >&2; }
error() { printf "\033[1;31m[ERROR]\033[0m %s\n" "$*" >&2; }

for cmd in curl uname grep sed mktemp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    error "Required command '$cmd' not found. Please install it and retry."
    exit 1
  fi
done

info "Fetching latest Talm release version..."

LATEST_VERSION=$(curl -fsSL "https://api.github.com/repos/cozystack/talm/releases/latest" | \
  grep '"tag_name":' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')

if [ -z "$LATEST_VERSION" ]; then
  error "Failed to fetch the latest Talm version."
  exit 1
fi

info "Latest version: $LATEST_VERSION"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64 | amd64) ARCH="amd64" ;;
  arm64 | aarch64) ARCH="arm64" ;;
  i386 | i686) ARCH="i386" ;;
  *)
    error "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

FILE="talm-$OS-$ARCH"
DOWNLOAD_URL="https://github.com/cozystack/talm/releases/download/$LATEST_VERSION/$FILE"

info "Downloading $FILE from $DOWNLOAD_URL..."

TMPDIR=$(mktemp -d)
cleanup() { rm -rf "$TMPDIR"; }
trap cleanup EXIT

curl -fL "$DOWNLOAD_URL" -o "$TMPDIR/talm"
chmod +x "$TMPDIR/talm"

if [ "$(id -u)" = 0 ]; then
  INSTALL_DIR="/usr/local/bin"
elif [ -w "/usr/local/bin" ]; then
  INSTALL_DIR="/usr/local/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
  if [ ! -d "$INSTALL_DIR" ]; then
    info "Creating directory $INSTALL_DIR"
    mkdir -p "$INSTALL_DIR"
  fi
  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      warn "Warning: $INSTALL_DIR is not in your PATH."
      ;;
  esac
fi

INSTALL_PATH="$INSTALL_DIR/talm"
mv "$TMPDIR/talm" "$INSTALL_PATH"

success "Talm installed successfully at $INSTALL_PATH"
info "You can now run: talm --help"