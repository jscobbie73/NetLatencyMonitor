# AWS Deployment Walkthrough

This guide walks you through deploying NetLatencyMonitor to 7 AWS EC2 t3.micro instances
spread across 6 regions: a controller in us-east-1, three hubs (us-east-1, us-west-1,
eu-central-1), and three spokes (ap-southeast-1 / Singapore, ap-northeast-1 / Tokyo,
sa-east-1 / São Paulo).

```
                        ┌──────────────────────────────────────────┐
                        │  CONTROLLER  (us-east-1)                 │
                        │  nlm-controller + Caddy + Litestream      │
                        │  Elastic IP → your-domain.com            │
                        └───────────┬──────────────────────────────┘
                                    │  HTTPS (TLS via Caddy)
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

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.22+ | https://go.dev/dl |
| Terraform | 1.5+ | https://developer.hashicorp.com/terraform/install |
| AWS CLI | 2.x | https://docs.aws.amazon.com/cli/latest/userguide/install-cliv2.html |
| jq | 1.6+ | `apt install jq` / `brew install jq` |
| An SSH key pair | — | `ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519` |

Your AWS credentials must have permission to create EC2 instances, EIPs, IAM users,
S3 buckets, and security groups. The simplest approach is to use a profile with
`AdministratorAccess` during initial provisioning.

```bash
aws configure          # or set AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
aws sts get-caller-identity   # verify credentials work
```

## Step 1 — Build the binaries

```bash
cd <repo-root>
make build
```

This produces `bin/nlm-controller` and `bin/nlm-agent` for the host platform.
`scripts/deploy.sh` cross-compiles for `linux/amd64` automatically, so you don't
need to build manually before deploying — but a local build is a good sanity check.

## Step 2 — Choose a unique S3 bucket name

Litestream replicates the controller's SQLite database to S3. The bucket name must
be globally unique across all AWS accounts:

```
nlm-backup-<your-initials>-<random-suffix>
# e.g. nlm-backup-js-4a8f
```

Keep this name handy; you'll need it in Step 3.

## Step 3 — Configure Terraform variables

```bash
cd deploy/aws
cp terraform.tfvars.example terraform.tfvars   # see below for creating this file
```

Create `deploy/aws/terraform.tfvars` with your values:

```hcl
# Path to the SSH public key to install on every node
ssh_public_key_path = "~/.ssh/id_ed25519.pub"

# NLM admin token — pick a strong random string (used to log in to the web UI)
admin_token = "CHANGE_ME_strong_random_token_here"

# S3 bucket for Litestream (must be globally unique)
litestream_s3_bucket = "nlm-backup-<your-initials>-<suffix>"

# Fully-qualified domain name you'll point at the controller's Elastic IP
# Caddy uses this to obtain a Let's Encrypt TLS certificate
controller_domain = "nlm.example.com"

# Email for Let's Encrypt notifications
acme_email = "you@example.com"
```

> `terraform.tfvars` is in `.gitignore` — never commit it; it contains secrets.

## Step 4 — Provision infrastructure

```bash
cd deploy/aws
terraform init
terraform plan    # review what will be created (≈ 40 resources)
terraform apply   # type "yes" when prompted
```

Terraform creates:
- 7 EC2 t3.micro instances (Ubuntu 24.04 LTS)
- 4 Elastic IPs (controller + 3 hubs)
- 6 key pairs (one per region)
- 6 security groups (one per region)
- 1 S3 bucket with versioning + AES-256 SSE
- 1 IAM user (`nlm-litestream`) with a scoped S3 policy + access key

`apply` typically finishes in 3–5 minutes.

After apply, note the outputs:

```bash
terraform output node_summary          # all IPs
terraform output controller_ip         # the IP you'll point DNS at
terraform output litestream_secret_access_key   # sensitive — shown once
```

## Step 5 — Point your DNS A record

Log in to your DNS provider and create an **A record**:

```
nlm.example.com   →   <controller_ip from Step 4>
TTL: 60 (low TTL so you can change quickly if needed)
```

DNS propagation usually takes 1–5 minutes. Verify with:

```bash
dig +short nlm.example.com
# should return the controller Elastic IP
```

You can proceed to Step 6 while DNS propagates; Caddy will retry ACME until it succeeds.

## Step 6 — Deploy the application

The deploy script reads Terraform outputs, cross-compiles, SCPs binaries to every node,
writes env files, registers all nodes via the admin API, and starts systemd services.

```bash
cd <repo-root>
export NLM_ADMIN_TOKEN="CHANGE_ME_strong_random_token_here"   # same as terraform.tfvars
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
10. Enables Caddy on the controller (TLS certificate is obtained automatically)

The script is **idempotent for re-deploys** except for node registration: if a node was
already registered the script skips re-registering it and logs a warning. If you need to
rotate secrets, delete and re-add the node via the admin API or web UI.

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
CTRL_IP=$(cd deploy/aws && terraform output -raw controller_ip)

# Basic health (no auth required)
curl https://nlm.example.com/healthz

# Readiness (chrony + Litestream lag check)
curl https://nlm.example.com/readyz

# Prometheus metrics (gate with NLM_METRICS_TOKEN if set)
curl https://nlm.example.com/metrics | grep nlm_
```

### Web UI

Open `https://nlm.example.com/ui/login` in a browser and enter the `admin_token`
from `terraform.tfvars`.

| Page | What you see |
|------|--------------|
| `/ui/` | Live latency matrix — hub × hub, 5-min rolling average, WebSocket real-time updates |
| `/ui/nodes` | All 7 nodes; add / disable / delete via the UI |

Wait 1–2 minutes after deploy for the first spoke cycles to complete; the matrix will
start populating as probe results arrive.

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

### Adding a new node

1. Add the EC2 instance + security group + EIP to `deploy/aws/main.tf` and `outputs.tf`
2. `terraform apply`
3. Register the node via the web UI or admin API
4. Push binaries + env files manually (or extend `scripts/deploy.sh`)

### Rotating the admin token

Update `NLM_ADMIN_TOKEN` in `/etc/nlm/controller.env` on the controller, then
`sudo systemctl restart nlm-controller`. The web UI session cookie will be invalidated
(users must log in again).

### Litestream restore

If the controller's SQLite database is lost, Litestream can restore it from S3:

```bash
# SSH into the controller
sudo systemctl stop nlm-controller nlm-litestream
sudo -u nlm litestream restore \
  -config /etc/litestream/litestream.yml \
  /var/lib/nlm/controller.db
sudo systemctl start nlm-litestream nlm-controller
```

### Teardown

```bash
cd deploy/aws
terraform destroy    # destroys all 7 instances, EIPs, bucket, IAM user
```

> The S3 bucket will fail to destroy if it still contains objects. Empty it first:
> `aws s3 rm s3://<bucket> --recursive`

## Cost estimate (us-east-1 on-demand pricing, May 2026)

| Resource | Count | $/month |
|----------|-------|---------|
| t3.micro EC2 | 7 | ~$7 × 7 = $49 |
| Elastic IPs (attached) | 4 | $0 |
| gp3 EBS (controller 20 GB + 6×10 GB) | 80 GB | ~$6.40 |
| S3 + data transfer | — | < $1 |
| **Total** | | **~$56/month** |

Spoke instances (3) don't need Elastic IPs since the controller only needs to reach
hubs (the spokes initiate outbound connections only).
