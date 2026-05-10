# AWS Deployment Walkthrough

This guide walks you through deploying NetLatencyMonitor to 7 AWS EC2 t3.micro instances
spread across 6 regions: a controller in us-east-1, three hubs (us-east-1, us-west-1,
eu-central-1), and three spokes (ap-southeast-1 / Singapore, ap-northeast-1 / Tokyo,
sa-east-1 / São Paulo). All supporting services (DNS, object storage, TLS certificates)
use AWS-native equivalents.

```
                        ┌──────────────────────────────────────────┐
                        │  CONTROLLER  (us-east-1)                 │
                        │  nlm-controller + Caddy + Litestream      │
                        │  Elastic IP ← Route 53 A record          │
                        └───────────┬──────────────────────────────┘
                                    │  HTTPS (TLS via Caddy + Let's Encrypt)
          ┌─────────────────────────┼─────────────────────────┐
          │                         │                         │
  ┌───────▼────────┐    ┌───────────▼──────┐    ┌────────────▼─────┐
  │  HUB us-east-1 │    │  HUB us-west-1   │    │  HUB eu-central-1│
  │  listener :8444│    │  listener :8444  │    │  listener :8444  │
  └────────────────┘    └──────────────────┘    └──────────────────┘
          ▲                         ▲                         ▲
          │   TCP probes from all spokes to all hubs          │
  ┌───────┴────────────────────┬────┴──────────┐             │
  │  SPOKE ap-southeast-1      │  SPOKE ap-northeast-1       │
  │  (Singapore)               │  (Tokyo)                    │
  └────────────────────────────┘             │               │
                                   SPOKE sa-east-1 ──────────┘
                                   (São Paulo)
```

## AWS Services Used

| Service | Purpose |
|---------|---------|
| EC2 (t3.micro) | 7 compute nodes across 6 regions |
| Elastic IPs | Static IPs for controller + 3 hubs |
| S3 | Litestream SQLite WAL replication |
| IAM | Scoped credentials for Litestream S3 access |
| Route 53 | DNS A record for the controller domain (managed by Terraform) |
| Let's Encrypt (via Caddy) | Automatic TLS certificate for the controller |

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.22+ | https://go.dev/dl |
| Terraform | 1.5+ | https://developer.hashicorp.com/terraform/install |
| AWS CLI | 2.x | https://docs.aws.amazon.com/cli/latest/userguide/install-cliv2.html |
| jq | 1.6+ | `apt install jq` / `brew install jq` |
| An SSH key pair | — | `ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519` |

### AWS account requirements

Your AWS credentials must have permission to create EC2 instances, EIPs, IAM users,
S3 buckets, security groups, and Route 53 records. A profile with `AdministratorAccess`
is the simplest option for initial provisioning.

```bash
aws configure          # or export AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
aws sts get-caller-identity   # verify credentials work
```

### Route 53 hosted zone

You need a domain managed in Route 53. If you don't have one:

```bash
# Option A — register a new domain directly in Route 53 (~$12/year for .com)
# Open the AWS Console → Route 53 → Register Domain

# Option B — delegate an existing domain to Route 53
# Create a hosted zone, then update your registrar's nameservers to the
# four NS records Route 53 assigns to the zone.
```

Find your Hosted Zone ID once the zone exists:

```bash
aws route53 list-hosted-zones-by-name \
  --dns-name example.com \
  --query "HostedZones[0].Id" \
  --output text
# returns /hostedzone/Z1234567890ABC — use only the trailing ID: Z1234567890ABC
```

## Step 1 — Build the binaries

```bash
cd <repo-root>
make build
```

This produces `bin/nlm-controller` and `bin/nlm-agent` for the host platform.
`scripts/deploy.sh` cross-compiles for `linux/amd64` automatically.

## Step 2 — Choose a unique S3 bucket name

Litestream replicates the controller's SQLite database to S3. The bucket name must
be globally unique across all AWS accounts:

```
nlm-backup-<your-initials>-<random-suffix>
# e.g. nlm-backup-js-4a8f
```

Keep this name handy; you'll need it in Step 3.

## Step 3 — Configure Terraform variables

Create `deploy/aws/terraform.tfvars`:

```bash
cd deploy/aws
cat > terraform.tfvars <<'EOF'
# Path to the SSH public key to install on every node
ssh_public_key_path = "~/.ssh/id_ed25519.pub"

# NLM admin token — use a strong random string (openssl rand -hex 32)
admin_token = "CHANGE_ME_strong_random_token_here"

# S3 bucket for Litestream (must be globally unique)
litestream_s3_bucket = "nlm-backup-<your-initials>-<suffix>"

# Fully-qualified domain name within your Route 53 hosted zone
# Caddy uses this to obtain a Let's Encrypt TLS certificate
controller_domain = "nlm.example.com"

# Route 53 Hosted Zone ID for the domain above (Z...)
route53_zone_id = "Z1234567890ABC"

# Email for Let's Encrypt certificate expiry notifications
acme_email = "you@example.com"
EOF
```

> `terraform.tfvars` is gitignored — never commit it; it contains secrets.

Generate a strong admin token with:

```bash
openssl rand -hex 32
```

## Step 4 — Provision infrastructure

```bash
cd deploy/aws
terraform init
terraform plan    # review what will be created (≈ 41 resources)
terraform apply   # type "yes" when prompted
```

Terraform creates:
- 7 EC2 t3.micro instances (Ubuntu 24.04 LTS)
- 4 Elastic IPs (controller + 3 hubs)
- 6 SSH key pairs (one per region)
- 6 security groups (one per region)
- 1 S3 bucket with versioning + AES-256 SSE
- 1 IAM user (`nlm-litestream`) with a scoped S3 policy + access key
- 1 Route 53 A record: `controller_domain` → controller Elastic IP (TTL 60s)

`apply` typically finishes in 3–5 minutes.

After apply, confirm the outputs:

```bash
terraform output node_summary            # all node IPs
terraform output controller_url          # https://nlm.example.com
terraform output route53_record_fqdn    # confirms the DNS record was created
terraform output litestream_secret_access_key   # sensitive — store securely
```

## Step 5 — Verify DNS propagation

Terraform creates the Route 53 record automatically. Route 53 changes are typically
visible within 60 seconds. Verify before proceeding:

```bash
# Check directly against the Route 53 authoritative nameservers (instant)
ZONE_ID="Z1234567890ABC"
NS=$(aws route53 get-hosted-zone --id $ZONE_ID \
  --query "DelegationSet.NameServers[0]" --output text)
dig +short nlm.example.com @$NS
# should return the controller Elastic IP immediately

# Check public resolution (may take up to 60s for SERVFAIL to clear)
dig +short nlm.example.com
```

You can proceed to Step 6 while public resolution catches up — Caddy will retry
the ACME challenge automatically once DNS resolves correctly.

## Step 6 — Deploy the application

The deploy script reads Terraform outputs, cross-compiles, SCPs binaries to every node,
writes env files, registers all nodes via the admin API, and starts systemd services.

```bash
cd <repo-root>
export NLM_ADMIN_TOKEN="CHANGE_ME_strong_random_token_here"   # same value as terraform.tfvars
scripts/deploy.sh
```

Optional overrides:

```bash
scripts/deploy.sh \
  --tf-dir deploy/aws \          # default
  --ssh-key ~/.ssh/id_ed25519    # default
```

What the script does, in order:

1. Reads all IPs and Litestream credentials from `terraform output -json`
2. Cross-compiles `bin/nlm-controller` and `bin/nlm-agent` for linux/amd64
3. Waits for SSH to be available on all 7 nodes (cloud-init takes ~90 s)
4. SCPs binaries + systemd units to each node and installs them under `/usr/local/bin/`
5. Writes `/etc/nlm/controller.env` and `/etc/nlm/litestream.env` on the controller
6. Starts `nlm-litestream` and `nlm-controller` on the controller
7. Calls `POST /api/v1/admin/nodes` to register all 6 agent nodes (hubs get an `address`
   field; spokes do not). Each registration returns a one-time minted secret.
8. Writes `/etc/nlm/agent.env` on each agent node with the minted secret
9. Starts `nlm-agent-listener` + `nlm-agent-hub` on hubs; enables the spoke timer on spokes
10. Enables Caddy on the controller (Let's Encrypt certificate obtained automatically)

The script is **idempotent for re-deploys** except for node registration: if a node was
already registered the script skips re-registering it and logs a warning. Rotate secrets
by deleting and re-adding the node via the admin API or web UI.

Expected output (truncated):

```
[deploy] Reading Terraform outputs from deploy/aws…
[deploy]   controller:    34.x.x.x
[deploy]   hub-use1:      54.x.x.x
…
[deploy] Building NLM binaries (linux/amd64)…
[deploy] Waiting for SSH on controller (34.x.x.x)…
[deploy]   SSH ready on controller
…
[deploy] Controller is healthy
[deploy] Registering node hub-use1 (role=hub)…
[deploy]   Registered hub-use1 (secret minted)
…
[deploy] Deployment complete
```

## Step 7 — Verify the deployment

### Controller health

```bash
CONTROLLER_URL=$(cd deploy/aws && terraform output -raw controller_url)

# Basic health (no auth required)
curl "${CONTROLLER_URL}/healthz"

# Readiness (chrony + Litestream lag check — may return 503 for ~30s on first start)
curl "${CONTROLLER_URL}/readyz"

# Prometheus metrics
curl "${CONTROLLER_URL}/metrics" | grep nlm_
```

### Web UI

Open the controller URL in a browser and navigate to `/ui/login`. Enter the
`admin_token` from `terraform.tfvars`.

```bash
echo "Open: $(cd deploy/aws && terraform output -raw controller_url)/ui/login"
```

| Page | What you see |
|------|--------------|
| `/ui/` | Live latency matrix — hub × hub, 5-min rolling average, WebSocket real-time updates |
| `/ui/nodes` | All 7 nodes; add / disable / delete via the UI |

Wait 1–2 minutes for the first spoke cycles to complete; the matrix will start
populating as probe results arrive.

### SSH into a node

```bash
SSH_KEY=~/.ssh/id_ed25519

# Controller
ssh -i $SSH_KEY ubuntu@$(cd deploy/aws && terraform output -raw controller_ip)
  sudo journalctl -u nlm-controller -f

# Hub (us-east-1)
ssh -i $SSH_KEY ubuntu@$(cd deploy/aws && terraform output -raw hub_use1_ip)
  sudo journalctl -u nlm-agent-hub -f

# Spoke (Singapore)
ssh -i $SSH_KEY ubuntu@$(cd deploy/aws && terraform output -raw spoke_apse1_ip)
  sudo journalctl -u nlm-agent-spoke -f
```

## Ongoing operations

### Updating binaries

Re-run `scripts/deploy.sh` after `git pull && make build`. The script skips node
registration (nodes already exist) and restarts services cleanly.

### Updating the Route 53 record

The Route 53 A record is Terraform-managed. If you replace the controller instance
(new EIP), run `terraform apply` — it will update the record automatically.

### Adding a new node

1. Add the EC2 instance + security group + EIP to `deploy/aws/main.tf` and `outputs.tf`
2. `terraform apply`
3. Register the node via the web UI or admin API
4. Run `scripts/deploy.sh` (idempotent — existing nodes are skipped)

### Rotating the admin token

1. Generate a new token: `openssl rand -hex 32`
2. Update `NLM_ADMIN_TOKEN` in `/etc/nlm/controller.env` on the controller
3. `sudo systemctl restart nlm-controller`
4. The web UI session cookie is invalidated — all users must log in again

### Litestream restore

If the controller's SQLite database is lost, restore from S3:

```bash
BUCKET=$(cd deploy/aws && terraform output -raw node_summary | jq -r '.controller')
# SSH into the controller
ssh -i ~/.ssh/id_ed25519 ubuntu@$CTRL_IP

sudo systemctl stop nlm-controller nlm-litestream
sudo -u nlm litestream restore \
  -config /etc/litestream/litestream.yml \
  /var/lib/nlm/controller.db
sudo systemctl start nlm-litestream nlm-controller
```

Verify the S3 backup contents directly:

```bash
aws s3 ls s3://<your-bucket>/db/ --recursive | tail -5
```

### Teardown

```bash
cd deploy/aws
# Empty the S3 bucket first (terraform destroy will fail otherwise)
BUCKET=$(terraform output -raw node_summary | python3 -c \
  "import sys,json; d=json.load(sys.stdin); print(list(d.values())[0])" 2>/dev/null || echo "")
aws s3 rm s3://$(terraform show -json | \
  python3 -c "import sys,json; r=json.load(sys.stdin)['values']['root_module']['resources']; \
  print(next(x['values']['bucket'] for x in r if x['type']=='aws_s3_bucket'))") --recursive

terraform destroy    # destroys all EC2, EIPs, S3 bucket, IAM user, Route 53 record
```

## Cost estimate (on-demand pricing, May 2026)

| Resource | Detail | $/month |
|----------|--------|---------|
| EC2 t3.micro × 7 | ~$0.0104/hr each | ~$53 |
| Elastic IPs (attached) | 4 attached to running instances | $0 |
| gp3 EBS | 20 GB controller + 6 × 10 GB agents | ~$6.40 |
| S3 | WAL segments + snapshots (< 1 GB typical) | < $1 |
| Route 53 hosted zone | $0.50/zone + $0.40/1M queries | < $1 |
| Data transfer | Cross-region probe traffic (minimal) | ~$1–3 |
| **Total** | | **~$62/month** |

Spoke instances do not need Elastic IPs — they only initiate outbound connections
to hubs and the controller.
