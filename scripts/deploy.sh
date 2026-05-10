#!/usr/bin/env bash
# deploy.sh — build NLM binaries locally and push them to every AWS node.
#
# Usage:
#   cd <repo-root>
#   scripts/deploy.sh [--tf-dir deploy/aws] [--ssh-key ~/.ssh/id_ed25519]
#
# Prerequisites:
#   - terraform >= 1.5 in PATH, already applied (state is live)
#   - go >= 1.22 in PATH
#   - SSH private key matching the public key used in Terraform
#   - jq in PATH

set -euo pipefail

# ── Defaults (override with flags or env vars) ─────────────────────────────────
TF_DIR="${NLM_TF_DIR:-deploy/aws}"
SSH_KEY="${NLM_SSH_KEY:-${HOME}/.ssh/id_ed25519}"
SSH_USER="${NLM_SSH_USER:-ubuntu}"
CONTROLLER_URL=""          # resolved from TF outputs; override with NLM_CONTROLLER_URL
ADMIN_TOKEN="${NLM_ADMIN_TOKEN:-}"
LISTENER_PORT="${NLM_LISTENER_PORT:-8444}"

# ── Argument parsing ───────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tf-dir)  TF_DIR="$2";  shift 2 ;;
    --ssh-key) SSH_KEY="$2"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 1 ;;
  esac
done

# ── Helpers ────────────────────────────────────────────────────────────────────
log()  { echo "[deploy] $*"; }
die()  { echo "[deploy] ERROR: $*" >&2; exit 1; }

ssh_cmd() {
  local host="$1"; shift
  ssh -i "$SSH_KEY" \
      -o StrictHostKeyChecking=no \
      -o ConnectTimeout=15 \
      -o BatchMode=yes \
      "${SSH_USER}@${host}" "$@"
}

scp_file() {
  local src="$1" dst_host="$2" dst_path="$3"
  scp -i "$SSH_KEY" \
      -o StrictHostKeyChecking=no \
      -o ConnectTimeout=15 \
      -q "$src" "${SSH_USER}@${dst_host}:${dst_path}"
}

wait_for_ssh() {
  local host="$1" label="$2"
  log "Waiting for SSH on ${label} (${host})…"
  local attempts=0
  until ssh_cmd "$host" true 2>/dev/null; do
    attempts=$((attempts + 1))
    [[ $attempts -ge 30 ]] && die "SSH to ${label} timed out after 5 minutes"
    sleep 10
  done
  log "  SSH ready on ${label}"
}

# ── Step 1: read Terraform outputs ────────────────────────────────────────────
log "Reading Terraform outputs from ${TF_DIR}…"
TF_OUT=$(cd "$TF_DIR" && terraform output -json)

CONTROLLER_IP=$(echo "$TF_OUT" | jq -r '.controller_ip.value')
HUB_USE1_IP=$(echo "$TF_OUT"   | jq -r '.hub_use1_ip.value')
HUB_USW1_IP=$(echo "$TF_OUT"   | jq -r '.hub_usw1_ip.value')
HUB_EUC1_IP=$(echo "$TF_OUT"   | jq -r '.hub_euc1_ip.value')
SPOKE_APSE1_IP=$(echo "$TF_OUT" | jq -r '.spoke_apse1_ip.value')
SPOKE_APNE1_IP=$(echo "$TF_OUT" | jq -r '.spoke_apne1_ip.value')
SPOKE_SAE1_IP=$(echo "$TF_OUT"  | jq -r '.spoke_sae1_ip.value')
LS_KEY_ID=$(echo "$TF_OUT"     | jq -r '.litestream_access_key_id.value')
LS_SECRET=$(echo "$TF_OUT"     | jq -r '.litestream_secret_access_key.value')
LS_BUCKET=$(echo "$TF_OUT"     | jq -r '.node_summary.value' | jq -r 'keys[0]' 2>/dev/null || true)
# Re-read S3 bucket from variables (outputs don't expose it directly)
LS_BUCKET=$(cd "$TF_DIR" && terraform show -json | jq -r '.values.root_module.resources[] | select(.type=="aws_s3_bucket") | .values.bucket')
CONTROLLER_URL="${NLM_CONTROLLER_URL:-$(echo "$TF_OUT" | jq -r '.controller_url.value')}"

[[ -z "$ADMIN_TOKEN" ]] && die "NLM_ADMIN_TOKEN is not set. Export it before running this script."
[[ "$CONTROLLER_IP"   == "null" ]] && die "controller_ip output is null — did terraform apply succeed?"

log "  controller:    ${CONTROLLER_IP}"
log "  hub-use1:      ${HUB_USE1_IP}"
log "  hub-usw1:      ${HUB_USW1_IP}"
log "  hub-euc1:      ${HUB_EUC1_IP}"
log "  spoke-apse1:   ${SPOKE_APSE1_IP}"
log "  spoke-apne1:   ${SPOKE_APNE1_IP}"
log "  spoke-sae1:    ${SPOKE_SAE1_IP}"
log "  controller URL: ${CONTROLLER_URL}"
log "  S3 bucket:     ${LS_BUCKET}"

# ── Step 2: build binaries ─────────────────────────────────────────────────────
log "Building NLM binaries (linux/amd64)…"
GOOS=linux GOARCH=amd64 make build
log "  bin/nlm-controller  $(stat -c%s bin/nlm-controller | numfmt --to=iec) bytes"
log "  bin/nlm-agent       $(stat -c%s bin/nlm-agent      | numfmt --to=iec) bytes"

# ── Step 3: define node metadata ──────────────────────────────────────────────
declare -A NODE_IP NODE_ROLE NODE_ID NODE_ADDR

NODE_IP[controller]="$CONTROLLER_IP"
NODE_ROLE[controller]="controller"
NODE_ID[controller]="controller"   # not registered as an agent node
NODE_ADDR[controller]=""

NODE_IP[hub-use1]="$HUB_USE1_IP"
NODE_ROLE[hub-use1]="hub"
NODE_ID[hub-use1]="hub-use1"
NODE_ADDR[hub-use1]="${HUB_USE1_IP}:${LISTENER_PORT}"

NODE_IP[hub-usw1]="$HUB_USW1_IP"
NODE_ROLE[hub-usw1]="hub"
NODE_ID[hub-usw1]="hub-usw1"
NODE_ADDR[hub-usw1]="${HUB_USW1_IP}:${LISTENER_PORT}"

NODE_IP[hub-euc1]="$HUB_EUC1_IP"
NODE_ROLE[hub-euc1]="hub"
NODE_ID[hub-euc1]="hub-euc1"
NODE_ADDR[hub-euc1]="${HUB_EUC1_IP}:${LISTENER_PORT}"

NODE_IP[spoke-apse1]="$SPOKE_APSE1_IP"
NODE_ROLE[spoke-apse1]="spoke"
NODE_ID[spoke-apse1]="spoke-apse1"
NODE_ADDR[spoke-apse1]=""

NODE_IP[spoke-apne1]="$SPOKE_APNE1_IP"
NODE_ROLE[spoke-apne1]="spoke"
NODE_ID[spoke-apne1]="spoke-apne1"
NODE_ADDR[spoke-apne1]=""

NODE_IP[spoke-sae1]="$SPOKE_SAE1_IP"
NODE_ROLE[spoke-sae1]="spoke"
NODE_ID[spoke-sae1]="spoke-sae1"
NODE_ADDR[spoke-sae1]=""

AGENT_NODES=(hub-use1 hub-usw1 hub-euc1 spoke-apse1 spoke-apne1 spoke-sae1)
ALL_NODES=(controller hub-use1 hub-usw1 hub-euc1 spoke-apse1 spoke-apne1 spoke-sae1)

# ── Step 4: wait for all nodes to be SSH-ready ────────────────────────────────
log "Checking SSH connectivity on all nodes…"
for node in "${ALL_NODES[@]}"; do
  wait_for_ssh "${NODE_IP[$node]}" "$node"
done

# ── Step 5: push binaries + systemd units to every node ───────────────────────
push_files() {
  local node="$1" ip="${NODE_IP[$1]}" role="${NODE_ROLE[$1]}"
  log "Pushing files to ${node} (${ip})…"

  # Create staging dir
  ssh_cmd "$ip" "sudo mkdir -p /tmp/nlm-deploy"

  if [[ "$role" == "controller" ]]; then
    scp_file bin/nlm-controller "$ip" /tmp/nlm-deploy/nlm-controller
    scp_file deploy/systemd/nlm-controller.service "$ip" /tmp/nlm-deploy/nlm-controller.service
    scp_file deploy/systemd/nlm-litestream.service "$ip" /tmp/nlm-deploy/nlm-litestream.service
    scp_file deploy/caddy/Caddyfile                "$ip" /tmp/nlm-deploy/Caddyfile
    scp_file deploy/litestream/litestream.yml      "$ip" /tmp/nlm-deploy/litestream.yml

    ssh_cmd "$ip" "sudo install -o root -g root -m 755 /tmp/nlm-deploy/nlm-controller /usr/local/bin/nlm-controller"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-controller.service /etc/systemd/system/"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-litestream.service /etc/systemd/system/"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/Caddyfile /etc/caddy/Caddyfile"
    ssh_cmd "$ip" "sudo install -o root -g nlm  -m 640 /tmp/nlm-deploy/litestream.yml /etc/litestream/litestream.yml"
  else
    scp_file bin/nlm-agent "$ip" /tmp/nlm-deploy/nlm-agent
    scp_file deploy/systemd/nlm-agent-hub.service      "$ip" /tmp/nlm-deploy/nlm-agent-hub.service
    scp_file deploy/systemd/nlm-agent-listener.service "$ip" /tmp/nlm-deploy/nlm-agent-listener.service
    scp_file deploy/systemd/nlm-agent-spoke.service    "$ip" /tmp/nlm-deploy/nlm-agent-spoke.service
    scp_file deploy/systemd/nlm-agent-spoke.timer      "$ip" /tmp/nlm-deploy/nlm-agent-spoke.timer

    ssh_cmd "$ip" "sudo install -o root -g root -m 755 /tmp/nlm-deploy/nlm-agent /usr/local/bin/nlm-agent"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-agent-hub.service      /etc/systemd/system/"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-agent-listener.service /etc/systemd/system/"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-agent-spoke.service    /etc/systemd/system/"
    ssh_cmd "$ip" "sudo install -o root -g root -m 644 /tmp/nlm-deploy/nlm-agent-spoke.timer      /etc/systemd/system/"
  fi

  ssh_cmd "$ip" "sudo rm -rf /tmp/nlm-deploy"
  log "  Files installed on ${node}"
}

for node in "${ALL_NODES[@]}"; do
  push_files "$node"
done

# ── Step 6: write env files on the controller ─────────────────────────────────
log "Writing env files on controller…"
CONTROLLER_IP_VAL="$CONTROLLER_IP"

ssh_cmd "$CONTROLLER_IP" "sudo tee /etc/nlm/controller.env > /dev/null" <<EOF
NLM_CONTROLLER_DB_PATH=/var/lib/nlm/controller.db
NLM_CONTROLLER_LISTEN=:8080
NLM_ADMIN_TOKEN=${ADMIN_TOKEN}
NLM_CHRONYC_BINARY=/usr/bin/chronyc
EOF

ssh_cmd "$CONTROLLER_IP" "sudo tee /etc/nlm/litestream.env > /dev/null" <<EOF
LITESTREAM_ACCESS_KEY_ID=${LS_KEY_ID}
LITESTREAM_SECRET_ACCESS_KEY=${LS_SECRET}
LITESTREAM_S3_BUCKET=${LS_BUCKET}
LITESTREAM_S3_REGION=us-east-1
EOF

ssh_cmd "$CONTROLLER_IP" "sudo chmod 640 /etc/nlm/controller.env /etc/nlm/litestream.env"
ssh_cmd "$CONTROLLER_IP" "sudo chown root:nlm /etc/nlm/controller.env /etc/nlm/litestream.env"

# ── Step 7: register agent nodes via the admin API ────────────────────────────
# We hit the controller over HTTP on port 8080 (Caddy not yet active).
# Start controller first so we can register nodes.

log "Starting controller services…"
ssh_cmd "$CONTROLLER_IP" "sudo systemctl daemon-reload"
ssh_cmd "$CONTROLLER_IP" "sudo systemctl enable --now nlm-litestream"
ssh_cmd "$CONTROLLER_IP" "sudo systemctl enable --now nlm-controller"

# Wait for controller HTTP to be reachable
CTRL_HTTP="http://${CONTROLLER_IP}:8080"
log "Waiting for controller HTTP (${CTRL_HTTP}/healthz)…"
attempts=0
until curl -sf "${CTRL_HTTP}/healthz" > /dev/null 2>&1; do
  attempts=$((attempts + 1))
  [[ $attempts -ge 24 ]] && die "Controller did not become healthy in 2 minutes"
  sleep 5
done
log "  Controller is healthy"

# Register each agent node and capture its minted secret
declare -A NODE_SECRET

for node in "${AGENT_NODES[@]}"; do
  id="${NODE_ID[$node]}"
  role="${NODE_ROLE[$node]}"
  addr="${NODE_ADDR[$node]}"

  log "Registering node ${id} (role=${role})…"

  if [[ "$role" == "hub" ]]; then
    body=$(jq -n --arg id "$id" --arg role "$role" --arg addr "$addr" \
      '{"id":$id,"role":$role,"address":$addr}')
  else
    body=$(jq -n --arg id "$id" --arg role "$role" \
      '{"id":$id,"role":$role}')
  fi

  response=$(curl -sf \
    -H "Authorization: Bearer ${ADMIN_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "${CTRL_HTTP}/api/v1/admin/nodes") || {
      # 409 = already registered (re-deploy); fetch existing node and re-use token
      log "  Node ${id} already exists — skipping registration (secret unchanged)"
      continue
    }

  secret=$(echo "$response" | jq -r '.secret // empty')
  [[ -z "$secret" ]] && die "No secret in response for ${id}: ${response}"
  NODE_SECRET[$node]="$secret"
  log "  Registered ${id} (secret minted)"
done

# ── Step 8: write agent env files and start agent services ────────────────────
for node in "${AGENT_NODES[@]}"; do
  ip="${NODE_IP[$node]}"
  id="${NODE_ID[$node]}"
  role="${NODE_ROLE[$node]}"
  secret="${NODE_SECRET[$node]:-}"

  if [[ -z "$secret" ]]; then
    log "WARNING: No secret for ${node} — skipping env write (node was already registered; set manually)"
    continue
  fi

  log "Writing agent.env on ${node} (${ip})…"
  ssh_cmd "$ip" "sudo tee /etc/nlm/agent.env > /dev/null" <<EOF
NLM_NODE_ID=${id}
NLM_NODE_SECRET=${secret}
NLM_CONTROLLER_URL=${CONTROLLER_URL}
NLM_SPOOL_PATH=/var/lib/nlm/agent-spool.db
NLM_LISTENER_LISTEN=:${LISTENER_PORT}
EOF
  ssh_cmd "$ip" "sudo chmod 640 /etc/nlm/agent.env"
  ssh_cmd "$ip" "sudo chown root:nlm /etc/nlm/agent.env"

  log "Starting agent services on ${node}…"
  ssh_cmd "$ip" "sudo systemctl daemon-reload"

  if [[ "$role" == "hub" ]]; then
    ssh_cmd "$ip" "sudo systemctl enable --now nlm-agent-listener"
    ssh_cmd "$ip" "sudo systemctl enable --now nlm-agent-hub"
  else
    # spoke: run once immediately, then the timer takes over
    ssh_cmd "$ip" "sudo systemctl enable --now nlm-agent-spoke.timer"
    ssh_cmd "$ip" "sudo systemctl start nlm-agent-spoke.service || true"
  fi

  log "  Services started on ${node}"
done

# ── Step 9: enable Caddy on the controller ────────────────────────────────────
log "Enabling Caddy on controller…"
ssh_cmd "$CONTROLLER_IP" "sudo systemctl enable --now caddy"

# ── Done ──────────────────────────────────────────────────────────────────────
log ""
log "┌─────────────────────────────────────────────────────────────┐"
log "│  Deployment complete                                        │"
log "├─────────────────────────────────────────────────────────────┤"
log "│  Controller HTTP (internal): http://${CONTROLLER_IP}:8080"
log "│  Controller HTTPS (public):  ${CONTROLLER_URL}"
log "│"
log "│  Next steps:"
log "│  1. Point your DNS A record for the controller domain"
log "│     to ${CONTROLLER_IP}"
log "│  2. Wait ~60 s for Caddy to obtain a Let's Encrypt cert"
log "│  3. Open ${CONTROLLER_URL}/ui/login"
log "│     and log in with your ADMIN_TOKEN"
log "│"
log "│  To check service status on the controller:"
log "│    ssh -i ${SSH_KEY} ${SSH_USER}@${CONTROLLER_IP}"
log "│    sudo systemctl status nlm-controller nlm-litestream caddy"
log "└─────────────────────────────────────────────────────────────┘"
