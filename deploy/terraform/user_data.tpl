#!/bin/bash
set -euo pipefail
# Cloud-init bootstrap for NLM nodes (Ubuntu 24.04).
# NLM binaries are installed separately via 'make install' or CI/CD.

apt-get update -qq
apt-get install -y --no-install-recommends \
  chrony \
  ca-certificates \
  curl \
  jq

# NLM system user and data directory.
useradd --system --home /var/lib/nlm --shell /usr/sbin/nologin nlm 2>/dev/null || true
mkdir -p /var/lib/nlm /etc/nlm
chown nlm:nlm /var/lib/nlm
chmod 750 /var/lib/nlm

%{ if role == "controller" ~}
# ── Controller extras ─────────────────────────────────────────────────────────

# Install Litestream.
LITESTREAM_VERSION="0.3.13"
curl -fsSL \
  "https://github.com/benbjohnson/litestream/releases/download/v$${LITESTREAM_VERSION}/litestream-v$${LITESTREAM_VERSION}-linux-amd64.tar.gz" \
  | tar -xz -C /usr/local/bin litestream
chmod +x /usr/local/bin/litestream
mkdir -p /etc/litestream
chown root:nlm /etc/litestream
chmod 750 /etc/litestream

# Install Caddy.
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] \
https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
  > /etc/apt/sources.list.d/caddy-stable.list
apt-get update -qq
apt-get install -y caddy
mkdir -p /var/log/caddy
chown caddy:caddy /var/log/caddy

%{ endif ~}

echo "nlm bootstrap complete — role=${role} version=${nlm_version}"
