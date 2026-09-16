#!/usr/bin/env bash
#
# Firefly One-Line Installer
# High-Concurrency OpenAI & Anthropic AI Reverse Proxy & Gateway
#
# Supported Distributions: Debian, Ubuntu, Fedora, CentOS, RHEL, Rocky, AlmaLinux, Arch, openSUSE, Alpine
#
# Quick Install:
#   curl -fsSL https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.sh | bash
#
# Environment Configuration (Optional):
#   FIREFLY_VERSION=v1.0.0          # Specific version tag (default: latest release)
#   FIREFLY_ADDR=0.0.0.0:8080       # Listen address
#   FIREFLY_CONFIG_DIR=/etc/firefly # Config directory
#   FIREFLY_USER=firefly            # System daemon user
#   SKIP_SERVICE=1                  # Install binaries only (skip systemd setup)
#
set -euo pipefail

REPO="dickymuliafiqri/firefly"
DEFAULT_PORT="8080"
FIREFLY_CONFIG_DIR="${FIREFLY_CONFIG_DIR:-/etc/firefly}"
FIREFLY_ADDR="${FIREFLY_ADDR:-0.0.0.0:${DEFAULT_PORT}}"
FIREFLY_USER="${FIREFLY_USER:-firefly}"
SKIP_SERVICE="${SKIP_SERVICE:-0}"

# -----------------------------------------------------------------------------
# Color and Terminal Helpers
# -----------------------------------------------------------------------------
setup_colors() {
  if [ -t 1 ] && command -v tput >/dev/null 2>&1; then
    ncolors=$(tput colors 2>/dev/null || echo 0)
    if [ "$ncolors" -ge 8 ]; then
      BOLD="$(tput bold 2>/dev/null || true)"
      CYAN="$(tput setaf 6 2>/dev/null || true)"
      GREEN="$(tput setaf 2 2>/dev/null || true)"
      YELLOW="$(tput setaf 3 2>/dev/null || true)"
      RED="$(tput setaf 1 2>/dev/null || true)"
      RESET="$(tput sgr0 2>/dev/null || true)"
      return
    fi
  fi
  BOLD="" CYAN="" GREEN="" YELLOW="" RED="" RESET=""
}
setup_colors

info()    { echo -e "${CYAN}::${RESET} $*"; }
step()    { echo -e "${BOLD}${CYAN}==>${RESET} ${BOLD}$*${RESET}"; }
success() { echo -e "${GREEN}✓${RESET} $*"; }
warn()    { echo -e "${YELLOW}!${RESET} $*"; }
error()   { echo -e "${RED}✗ Error:${RESET} $*" >&2; }

# -----------------------------------------------------------------------------
# Privilege Verification (Root or Sudo)
# -----------------------------------------------------------------------------
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    error "This installer requires superuser privileges. Please run as root or install sudo."
    exit 1
  fi
fi

# -----------------------------------------------------------------------------
# OS & Architecture Detection
# -----------------------------------------------------------------------------
detect_platform() {
  OS=$(uname -s | tr '[:upper:]' '[:lower:]')
  ARCH=$(uname -m)

  case "$ARCH" in
    x86_64|amd64)
      ARCH="amd64"
      ;;
    aarch64|arm64)
      ARCH="arm64"
      ;;
    *)
      error "Unsupported architecture: $ARCH. Supported: amd64 (x86_64), arm64 (aarch64)."
      exit 1
      ;;
  esac

  case "$OS" in
    linux)
      ;;
    darwin)
      info "Detected macOS (${OS}_${ARCH}). Systemd will be skipped; binary will be installed to /usr/local/bin."
      SKIP_SERVICE=1
      ;;
    *)
      error "Unsupported operating system: $OS. Supported: Linux, macOS."
      exit 1
      ;;
  esac

  info "Detected platform: ${BOLD}${OS}/${ARCH}${RESET}"
}

# -----------------------------------------------------------------------------
# Dependency Resolution
# -----------------------------------------------------------------------------
ensure_dependencies() {
  local missing=()
  for cmd in curl tar; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      missing+=("$cmd")
    fi
  done

  if [ ${#missing[@]} -gt 0 ]; then
    info "Installing missing dependencies: ${missing[*]}..."
    if command -v apt-get >/dev/null 2>&1; then
      $SUDO apt-get update -qq && $SUDO apt-get install -y -qq "${missing[@]}"
    elif command -v dnf >/dev/null 2>&1; then
      $SUDO dnf install -y -q "${missing[@]}"
    elif command -v yum >/dev/null 2>&1; then
      $SUDO yum install -y -q "${missing[@]}"
    elif command -v pacman >/dev/null 2>&1; then
      $SUDO pacman -Sy --noconfirm "${missing[@]}"
    elif command -v zypper >/dev/null 2>&1; then
      $SUDO zypper --quiet install -y "${missing[@]}"
    elif command -v apk >/dev/null 2>&1; then
      $SUDO apk add --no-cache "${missing[@]}"
    else
      warn "Unable to auto-install dependencies. Please manually install: ${missing[*]}"
    fi
  fi
}

# -----------------------------------------------------------------------------
# Release Version Resolution
# -----------------------------------------------------------------------------
resolve_version() {
  if [ -n "${FIREFLY_VERSION:-}" ]; then
    VERSION="$FIREFLY_VERSION"
    return
  fi

  info "Checking latest Firefly release on GitHub..."

  # 1. Resolve through redirect URL (no GitHub API rate limits)
  local final_url
  final_url=$(curl -fsSL -o /dev/null -w "%{url_effective}" "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)
  if [ -n "$final_url" ] && [ "$final_url" != "https://github.com/${REPO}/releases/latest" ]; then
    VERSION=$(basename "$final_url")
  fi

  # 2. Fallback to API check
  if [ -z "${VERSION:-}" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/' || true)
  fi

  # 3. Fallback default
  if [ -z "${VERSION:-}" ]; then
    VERSION="v1.0.0"
  fi

  info "Resolved version: ${BOLD}${VERSION}${RESET}"
}

# -----------------------------------------------------------------------------
# Download and Install Binaries
# -----------------------------------------------------------------------------
install_binaries() {
  step "Downloading Firefly ${VERSION} (${OS}/${ARCH})..."

  local archive="firefly_${VERSION}_${OS}_${ARCH}.tar.gz"
  local url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"
  local tmp_dir
  tmp_dir=$(mktemp -d)
  trap '$SUDO rm -rf "$tmp_dir"' EXIT

  local downloaded=false

  if curl -fsSL "$url" -o "${tmp_dir}/${archive}" 2>/dev/null; then
    downloaded=true
  else
    warn "Prebuilt release binary not found at $url"
    # Check if we are running in a firefly git clone with Go installed
    if command -v go >/dev/null 2>&1 && [ -f "./cmd/firefly/main.go" ]; then
      info "Found local Firefly source with Go toolchain. Compiling from source..."
      go build -trimpath -ldflags="-s -w -X main.Version=${VERSION}" -o "${tmp_dir}/firefly" ./cmd/firefly
      if [ -f "./cmd/loadtest/main.go" ]; then
        go build -trimpath -ldflags="-s -w" -o "${tmp_dir}/loadtest" ./cmd/loadtest
      fi
      downloaded=true
    else
      error "Could not download Firefly release ${VERSION}. Please verify https://github.com/${REPO}/releases"
      exit 1
    fi
  fi

  if [ -f "${tmp_dir}/${archive}" ]; then
    tar -xzf "${tmp_dir}/${archive}" -C "${tmp_dir}"
  fi

  step "Installing executables to /usr/local/bin..."
  $SUDO mkdir -p /usr/local/bin

  if [ -f "${tmp_dir}/firefly" ]; then
    $SUDO install -m 0755 "${tmp_dir}/firefly" /usr/local/bin/firefly
    success "Installed /usr/local/bin/firefly"
  else
    error "Binary firefly not found in extracted archive."
    exit 1
  fi

  if [ -f "${tmp_dir}/loadtest" ]; then
    $SUDO install -m 0755 "${tmp_dir}/loadtest" /usr/local/bin/loadtest
    success "Installed /usr/local/bin/loadtest (CLI Benchmark Tool)"
  fi
}

# -----------------------------------------------------------------------------
# Configuration Directory & Permissions
# -----------------------------------------------------------------------------
setup_config() {
  step "Setting up configuration in ${FIREFLY_CONFIG_DIR}..."

  $SUDO mkdir -p "${FIREFLY_CONFIG_DIR}"

  # Create environment file if not already present
  local env_file="${FIREFLY_CONFIG_DIR}/firefly.env"
  if [ ! -f "$env_file" ]; then
    $SUDO tee "$env_file" >/dev/null <<EOF
# Firefly Service Environment Configuration
# Documentation: https://github.com/${REPO}

FIREFLY_ADDR=${FIREFLY_ADDR}
FIREFLY_CONFIG_DIR=${FIREFLY_CONFIG_DIR}
FIREFLY_LOG_LEVEL=info
FIREFLY_SHUTDOWN_GRACE_SECONDS=30

# Optional: Bearer token to protect admin endpoints (/metrics, /debug/*)
# FIREFLY_ADMIN_TOKEN=sk-admin-secret-token
EOF
    success "Created initial environment file: ${env_file}"
  fi

  # Create default JSON configuration templates if missing
  local files=(
    "upstreams.json:{\n  \"upstreams\": []\n}"
    "models.json:{\n  \"models\": []\n}"
    "tenants.json:{\n  \"tenants\": []\n}"
    "combos.json:{\n  \"combos\": []\n}"
  )

  for entry in "${files[@]}"; do
    local fname="${entry%%:*}"
    local fcontent="${entry#*:}"
    local fpath="${FIREFLY_CONFIG_DIR}/${fname}"
    if [ ! -f "$fpath" ]; then
      echo -e "$fcontent" | $SUDO tee "$fpath" >/dev/null
    fi
  done

  # Setup dedicated daemon user on Linux
  if [ "$OS" = "linux" ]; then
    if ! id -u "$FIREFLY_USER" >/dev/null 2>&1; then
      info "Creating dedicated service user '${FIREFLY_USER}'..."
      if command -v useradd >/dev/null 2>&1; then
        $SUDO useradd --system --shell /usr/sbin/nologin --no-create-home "$FIREFLY_USER" 2>/dev/null || \
        $SUDO useradd --system --shell /sbin/nologin --no-create-home "$FIREFLY_USER" 2>/dev/null || true
      elif command -v adduser >/dev/null 2>&1; then
        $SUDO adduser -S -D -H "$FIREFLY_USER" 2>/dev/null || true
      fi
    fi

    # Set directory ownership so hot-swap changes from dashboard persist cleanly
    if id -u "$FIREFLY_USER" >/dev/null 2>&1; then
      $SUDO chown -R "${FIREFLY_USER}:${FIREFLY_USER}" "${FIREFLY_CONFIG_DIR}"
      $SUDO chmod 0750 "${FIREFLY_CONFIG_DIR}"
      success "Config directory ownership set to ${FIREFLY_USER}:${FIREFLY_USER}"
    fi
  fi
}

# -----------------------------------------------------------------------------
# Background Service Setup (systemd / OpenRC)
# -----------------------------------------------------------------------------
setup_service() {
  if [ "$SKIP_SERVICE" = "1" ]; then
    return
  fi

  # Check for systemd
  if [ -d /run/systemd/system ] || command -v systemctl >/dev/null 2>&1; then
    step "Configuring systemd background service..."

    local unit_file="/etc/systemd/system/firefly.service"
    local run_user="root"
    if id -u "$FIREFLY_USER" >/dev/null 2>&1; then
      run_user="$FIREFLY_USER"
    fi

    $SUDO tee "$unit_file" >/dev/null <<EOF
[Unit]
Description=Firefly High-Concurrency AI Gateway
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${run_user}
Group=${run_user}
EnvironmentFile=-${FIREFLY_CONFIG_DIR}/firefly.env
ExecStart=/usr/local/bin/firefly -config-dir=${FIREFLY_CONFIG_DIR} -addr=${FIREFLY_ADDR}
Restart=always
RestartSec=3s
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# Security hardening
ProtectSystem=full
ReadWritePaths=${FIREFLY_CONFIG_DIR}

[Install]
WantedBy=multi-user.target
EOF

    success "Created systemd unit: ${unit_file}"

    step "Enabling and starting firefly service in background..."
    $SUDO systemctl daemon-reload
    $SUDO systemctl enable firefly.service
    $SUDO systemctl restart firefly.service

    # Verify service health
    sleep 1.5
    if $SUDO systemctl is-active --quiet firefly.service; then
      success "Firefly background daemon is ACTIVE and running!"
    else
      warn "Service started but status check returned non-active. Check logs: journalctl -u firefly -n 50"
    fi
    return
  fi

  # Check for OpenRC (e.g. Alpine Linux)
  if [ -d /etc/init.d ] && command -v rc-service >/dev/null 2>&1; then
    step "Configuring OpenRC service..."
    local init_file="/etc/init.d/firefly"

    $SUDO tee "$init_file" >/dev/null <<'EOF'
#!/sbin/openrc-run
description="Firefly AI Gateway"
command="/usr/local/bin/firefly"
command_args="-config-dir=/etc/firefly -addr=0.0.0.0:8080"
command_background="yes"
command_user="firefly:firefly"
pidfile="/run/firefly.pid"

depend() {
  need net
  after firewall
}
EOF
    $SUDO chmod +x "$init_file"
    $SUDO rc-update add firefly default 2>/dev/null || true
    $SUDO rc-service firefly restart || true
    success "OpenRC service configured and started."
    return
  fi

  warn "No systemd or OpenRC detected (e.g. container environment). Skipping background service setup."
}

# -----------------------------------------------------------------------------
# Primary IP Address Detection
# -----------------------------------------------------------------------------
get_primary_ip() {
  local ip=""
  if command -v hostname >/dev/null 2>&1; then
    ip=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
  fi
  if [ -z "$ip" ] && command -v ip >/dev/null 2>&1; then
    ip=$(ip route get 1.1.1.1 2>/dev/null | awk '{print $7}' || true)
  fi
  if [ -z "$ip" ]; then
    ip="127.0.0.1"
  fi
  echo "$ip"
}

# -----------------------------------------------------------------------------
# Main Execution Flow
# -----------------------------------------------------------------------------
main() {
  echo ""
  echo -e "${BOLD}${CYAN}  _____ _           __ _       ${RESET}"
  echo -e "${BOLD}${CYAN} |  ___(_)_ __ ___ / _| |_   _ ${RESET}"
  echo -e "${BOLD}${CYAN} | |_  | | '__/ _ \ |_| | | | |${RESET}"
  echo -e "${BOLD}${CYAN} |  _| | | | |  __/  _| | |_| |${RESET}"
  echo -e "${BOLD}${CYAN} |_|   |_|_|  \___|_| |_|\__, |${RESET}"
  echo -e "${BOLD}${CYAN}                         |___/ ${RESET}"
  echo -e "${BOLD} Firefly High-Concurrency AI Gateway Installer${RESET}"
  echo ""

  detect_platform
  ensure_dependencies
  resolve_version
  install_binaries
  setup_config
  setup_service

  local host_ip
  host_ip=$(get_primary_ip)
  local port="${FIREFLY_ADDR##*:}"

  echo ""
  echo -e "${BOLD}${GREEN}===================================================================${RESET}"
  echo -e "${BOLD}${GREEN}  Firefly has been successfully installed and started!${RESET}"
  echo -e "${BOLD}${GREEN}===================================================================${RESET}"
  echo ""
  echo -e "  ${BOLD}Dashboard URL:${RESET}      http://${host_ip}:${port}"
  echo -e "  ${BOLD}Default Password:${RESET}   12345678 (Configure in Settings)"
  echo -e "  ${BOLD}API Endpoint:${RESET}       http://${host_ip}:${port}/v1/chat/completions"
  echo -e "  ${BOLD}Config Directory:${RESET}   ${FIREFLY_CONFIG_DIR}"
  echo -e "  ${BOLD}Environment File:${RESET}   ${FIREFLY_CONFIG_DIR}/firefly.env"
  echo ""
  if [ "$SKIP_SERVICE" != "1" ]; then
    echo -e "  ${BOLD}Service Controls:${RESET}"
    echo -e "    Status:   ${CYAN}sudo systemctl status firefly${RESET}"
    echo -e "    Restart:  ${CYAN}sudo systemctl restart firefly${RESET}"
    echo -e "    Logs:     ${CYAN}sudo journalctl -u firefly -f${RESET}"
    echo ""
  fi
  echo -e "  ${BOLD}Quick Concurrency Test:${RESET}"
  echo -e "    ${CYAN}loadtest -mock -c 100 -profile low${RESET}"
  echo ""
}

main "$@"
