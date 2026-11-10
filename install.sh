#!/usr/bin/env bash
# Install the BSV noise / covering-fire service on a fresh Ubuntu LTS VPS.
#
# Safe defaults: BSV testnet, broadcast off, funding import off
# ("paused until you fund and opt in"). Mainnet broadcast requires explicit flags.
#
# Usage (from a clone of this repository, as a sudoer):
#   sudo ./install.sh --domain noise.example.com
#
# Options:
#   --domain FQDN          Optional. Writes a Caddy HTTPS site for this hostname.
#   --dir PATH             Install directory (default /opt/bsv-noise)
#   --network test|main    Default test. "main" still does not broadcast unless
#                          you also pass --enable-broadcast --allow-mainnet-broadcast.
#   --import-funding       Turn on Message Box funding import (still no spends).
#   --enable-broadcast     Turn on CreateAction broadcast (refused on mainnet
#                          without --allow-mainnet-broadcast).
#   --allow-mainnet-broadcast
#   --skip-caddy           Install Docker and the app only.
#   --skip-start           Write files and images, do not start the container.
#
# The server private key is generated on this machine, stored at
#   $DIR/secrets/server_private_key
# with mode 600, and is never printed.
set -euo pipefail

DOMAIN=""
INSTALL_DIR="/opt/bsv-noise"
NETWORK="test"
IMPORT_FUNDING="false"
BROADCAST="false"
ALLOW_MAINNET="false"
SKIP_CADDY="false"
SKIP_START="false"

usage() {
  sed -n '2,28p' "$0" | sed 's/^# \?//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    --dir) INSTALL_DIR="${2:-}"; shift 2 ;;
    --network) NETWORK="${2:-}"; shift 2 ;;
    --import-funding) IMPORT_FUNDING="true"; shift ;;
    --enable-broadcast) BROADCAST="true"; shift ;;
    --allow-mainnet-broadcast) ALLOW_MAINNET="true"; shift ;;
    --skip-caddy) SKIP_CADDY="true"; shift ;;
    --skip-start) SKIP_START="true"; shift ;;
    -h|--help) usage ;;
    *) echo "unknown option: $1" >&2; usage ;;
  esac
done

if [[ "$NETWORK" != "test" && "$NETWORK" != "main" ]]; then
  echo "error: --network must be test or main" >&2
  exit 1
fi
if [[ "$BROADCAST" == "true" && "$NETWORK" == "main" && "$ALLOW_MAINNET" != "true" ]]; then
  echo "error: mainnet broadcast requires --allow-mainnet-broadcast" >&2
  exit 1
fi

if [[ "${EUID}" -ne 0 ]]; then
  echo "error: re-run as root (sudo ./install.sh ...)" >&2
  exit 1
fi

# Resolve the repository this script lives in, even if invoked via sudo.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ ! -f "$SCRIPT_DIR/Dockerfile" || ! -f "$SCRIPT_DIR/docker-compose.yml" ]]; then
  echo "error: install.sh must be run from a checkout that contains Dockerfile and docker-compose.yml" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

echo "==> Installing apt packages (ca-certificates, curl, gnupg, jq)"
apt-get update -qq
apt-get install -y -qq ca-certificates curl gnupg jq debian-keyring debian-archive-keyring apt-transport-https >/dev/null

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    echo "==> Docker already installed: $(docker --version)"
    return
  fi
  echo "==> Installing Docker Engine and Compose plugin"
  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /etc/apt/keyrings/docker.gpg ]]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    chmod a+r /etc/apt/keyrings/docker.gpg
  fi
  local codename
  codename="$(. /etc/os-release && echo "${VERSION_CODENAME}")"
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${codename} stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
  systemctl enable --now docker
}

install_caddy() {
  if [[ "$SKIP_CADDY" == "true" ]]; then
    return
  fi
  if command -v caddy >/dev/null 2>&1; then
    echo "==> Caddy already installed: $(caddy version | head -n1)"
    return
  fi
  echo "==> Installing Caddy"
  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg ]]; then
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  fi
  curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq
  apt-get install -y -qq caddy >/dev/null
  systemctl enable caddy
}

install_docker
install_caddy

echo "==> Installing application files into ${INSTALL_DIR}"
mkdir -p "$INSTALL_DIR"
# Copy the repo into the install dir if we are not already running from there.
if [[ "$(cd "$SCRIPT_DIR" && pwd)" != "$(cd "$INSTALL_DIR" && pwd)" ]]; then
  # Preserve existing secrets/data if this is a re-run.
  tar -C "$SCRIPT_DIR" \
    --exclude './data' \
    --exclude './secrets' \
    --exclude './.git' \
    --exclude './console/node_modules' \
    --exclude './console/dist' \
    --exclude './scripts/register-handle/node_modules' \
    -cf - . | tar -C "$INSTALL_DIR" -xf -
fi
mkdir -p "$INSTALL_DIR/secrets" "$INSTALL_DIR/data" "$INSTALL_DIR/backups"
chmod 700 "$INSTALL_DIR/secrets"
chown 65532:65532 "$INSTALL_DIR/data"
chmod 755 "$INSTALL_DIR/data"

KEY_FILE="$INSTALL_DIR/secrets/server_private_key"
if [[ -f "$KEY_FILE" ]]; then
  echo "==> Existing server key found at $KEY_FILE (not rotated, not printed)"
else
  echo "==> Generating server private key on this VPS (not printed)"
  umask 077
  # 32-byte hex secp256k1 scalar. Written straight to the secret file.
  # openssl output is redirected; nothing is echoed.
  openssl rand -hex 32 >"$KEY_FILE"
  # Refuse to continue if the file is empty or the wrong length (64 hex + newline).
  if [[ "$(wc -c <"$KEY_FILE")" -lt 64 ]]; then
    echo "error: key generation produced an unexpectedly short file" >&2
    rm -f "$KEY_FILE"
    exit 1
  fi
fi
chmod 600 "$KEY_FILE"
chown root:root "$KEY_FILE"
# The container runs as uid 65532 and reads the key via a Docker secret, which
# is mounted from this file. Docker (root) can read it; do not chmod world-readable.

ENV_FILE="$INSTALL_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  umask 077
  cat >"$ENV_FILE" <<ENVEOF
NETWORK=${NETWORK}
BROADCAST=${BROADCAST}
ALLOW_MAINNET_BROADCAST=${ALLOW_MAINNET}
IMPORT_FUNDING=${IMPORT_FUNDING}
CONTROLLED_RECIPIENTS=true
TARGET_TPS=0.5
MESSAGE_BOX_URL=https://messagebox.bsvblockchain.tech
MESSAGE_BOX=payment_inbox
DATA_MOUNT=${INSTALL_DIR}/data
ENVEOF
  chmod 600 "$ENV_FILE"
  echo "==> Wrote $ENV_FILE (no private key in this file)"
else
  echo "==> Leaving existing $ENV_FILE in place"
fi

# Ensure compose uses the bind-mounted data dir for easy backups.
# DATA_MOUNT is already in .env; compose reads it.

echo "==> Building container image (this may take several minutes the first time)"
cd "$INSTALL_DIR"
# Prefer host network during the build so module downloads work on locked-down VPS NAT.
if docker buildx version >/dev/null 2>&1; then
  docker buildx build --network=host --load -t bsv-noise:local .
else
  docker build -t bsv-noise:local .
fi

# Tag a rollback handle if one does not already exist for this first install.
if ! docker image inspect bsv-noise:previous >/dev/null 2>&1; then
  docker tag bsv-noise:local bsv-noise:previous
fi

if [[ "$SKIP_START" != "true" ]]; then
  echo "==> Starting service"
  docker compose --env-file "$ENV_FILE" up -d --force-recreate
  echo "==> Waiting for health"
  ok=0
  for _ in $(seq 1 36); do
    if curl -fsS http://127.0.0.1:8741/healthz >/tmp/bsv-noise-health.json 2>/dev/null; then
      ok=1
      break
    fi
    sleep 5
  done
  if [[ "$ok" -ne 1 ]]; then
    echo "error: service did not become healthy on 127.0.0.1:8741" >&2
    docker compose --env-file "$ENV_FILE" logs --tail 80 >&2 || true
    exit 1
  fi
else
  echo "==> Skipping start (--skip-start)"
fi

# Identity public key from /healthz only — never from the secret file.
IDENTITY=""
if [[ -f /tmp/bsv-noise-health.json ]]; then
  IDENTITY="$(jq -r '.identity_key // .identity // empty' /tmp/bsv-noise-health.json 2>/dev/null || true)"
  rm -f /tmp/bsv-noise-health.json
fi

if [[ "$SKIP_CADDY" != "true" && -n "$DOMAIN" ]]; then
  SITE_FILE="/etc/caddy/sites/bsv-noise.caddy"
  mkdir -p /etc/caddy/sites
  cat >"$SITE_FILE" <<CADDYEOF
${DOMAIN} {
	encode gzip
	reverse_proxy 127.0.0.1:8741
}
CADDYEOF
  if [[ ! -f /etc/caddy/Caddyfile ]]; then
    printf 'import /etc/caddy/sites/*\n' > /etc/caddy/Caddyfile
  elif ! grep -q 'import /etc/caddy/sites/\*' /etc/caddy/Caddyfile; then
    if ! grep -q 'bsv-noise' /etc/caddy/Caddyfile; then
      printf '\n# bsv-noise\nimport /etc/caddy/sites/*\n' >> /etc/caddy/Caddyfile
    fi
  fi
  echo "==> Wrote $SITE_FILE"
  echo "    Point a DNS A/AAAA record for ${DOMAIN} at this VPS, then:"
  echo "      sudo systemctl reload caddy"
  if systemctl is-active --quiet caddy; then
    if caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1; then
      systemctl reload caddy || systemctl restart caddy || true
    else
      echo "    warning: Caddyfile validation failed; not reloading. Fix the file and reload manually."
    fi
  else
    systemctl start caddy || true
  fi
fi

# Discover a public IP for DNS guidance without assuming a cloud metadata service.
PUBLIC_IP="$(curl -4 -fsS --max-time 5 https://ifconfig.me 2>/dev/null || true)"
if [[ -z "$PUBLIC_IP" ]]; then
  PUBLIC_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
fi

echo
echo "=============================================="
echo " BSV noise service is installed"
echo "=============================================="
echo " Install dir:     $INSTALL_DIR"
echo " Network:         $NETWORK"
echo " Broadcast:       $BROADCAST"
echo " Import funding:  $IMPORT_FUNDING"
echo " Allow mainnet:   $ALLOW_MAINNET"
if [[ -n "$DOMAIN" ]]; then
  echo " Console URL:     https://${DOMAIN}/"
else
  echo " Console URL:     http://127.0.0.1:8741/  (SSH tunnel or add --domain)"
fi
if [[ -n "$IDENTITY" ]]; then
  echo " Identity pubkey: $IDENTITY"
  echo " Fund this identity with a BRC-29 / PeerPay payment to the Message Box"
  echo " host (payment_inbox). A plain P2PKH address is not a Message Box."
else
  echo " Identity pubkey: (start the service and read identity_key from /healthz)"
fi
if [[ -n "$PUBLIC_IP" && -n "$DOMAIN" ]]; then
  echo
  echo " DNS: create an A record  ${DOMAIN}  ->  ${PUBLIC_IP}"
  echo "      (and AAAA if you have a public IPv6 address)"
fi
echo
echo " The server private key is at $KEY_FILE (mode 600, not printed)."
echo " Enable funding import:  set IMPORT_FUNDING=true in $ENV_FILE and"
echo "   docker compose --env-file $ENV_FILE up -d"
echo " Enable broadcast later: set BROADCAST=true (and, on mainnet,"
echo "   NETWORK=main and ALLOW_MAINNET_BROADCAST=true) then recreate."
echo " Backup:  $INSTALL_DIR/scripts/backup.sh"
echo "=============================================="
