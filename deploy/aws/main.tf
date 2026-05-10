terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  required_version = ">= 1.5"

  # Recommended for team use — uncomment and fill in your bucket + region:
  # backend "s3" {
  #   bucket = "my-terraform-state"
  #   key    = "nlm/terraform.tfstate"
  #   region = "us-east-1"
  # }
}

# ── Provider aliases (one per region) ─────────────────────────────────────────
#
# Hubs   : us-east-1 (N. Virginia), us-west-1 (N. California), eu-central-1 (Frankfurt)
# Spokes : ap-southeast-1 (Singapore), ap-northeast-1 (Tokyo), sa-east-1 (São Paulo)
# Controller also lives in us-east-1.

provider "aws" { alias = "use1";  region = "us-east-1" }
provider "aws" { alias = "usw1";  region = "us-west-1" }
provider "aws" { alias = "euc1";  region = "eu-central-1" }
provider "aws" { alias = "apse1"; region = "ap-southeast-1" }
provider "aws" { alias = "apne1"; region = "ap-northeast-1" }
provider "aws" { alias = "sae1";  region = "sa-east-1" }

# ── Ubuntu 24.04 LTS AMI per region (Canonical account 099720109477) ──────────

data "aws_ami" "ubuntu_use1" {
  provider    = aws.use1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

data "aws_ami" "ubuntu_usw1" {
  provider    = aws.usw1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

data "aws_ami" "ubuntu_euc1" {
  provider    = aws.euc1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

data "aws_ami" "ubuntu_apse1" {
  provider    = aws.apse1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

data "aws_ami" "ubuntu_apne1" {
  provider    = aws.apne1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

data "aws_ami" "ubuntu_sae1" {
  provider    = aws.sae1
  most_recent = true
  owners      = ["099720109477"]
  filter { name = "name";                 values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"] }
  filter { name = "virtualization-type";  values = ["hvm"] }
  filter { name = "architecture";         values = ["x86_64"] }
}

# ── SSH key pairs (same public key uploaded to every region) ──────────────────

resource "aws_key_pair" "nlm_use1"  { provider = aws.use1;  key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }
resource "aws_key_pair" "nlm_usw1"  { provider = aws.usw1;  key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }
resource "aws_key_pair" "nlm_euc1"  { provider = aws.euc1;  key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }
resource "aws_key_pair" "nlm_apse1" { provider = aws.apse1; key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }
resource "aws_key_pair" "nlm_apne1" { provider = aws.apne1; key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }
resource "aws_key_pair" "nlm_sae1"  { provider = aws.sae1;  key_name = "nlm-deploy"; public_key = file(var.ssh_public_key_path) }

# ── Security groups ────────────────────────────────────────────────────────────

resource "aws_security_group" "controller" {
  provider    = aws.use1
  name        = "nlm-controller"
  description = "NLM controller — HTTP/HTTPS (Caddy) + SSH"

  ingress { description = "SSH";   from_port = 22;  to_port = 22;  protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { description = "HTTP";  from_port = 80;  to_port = 80;  protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { description = "HTTPS"; from_port = 443; to_port = 443; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0; to_port = 0; protocol = "-1"; cidr_blocks = ["0.0.0.0/0"] }

  tags = { Name = "nlm-controller"; app = "nlm" }
}

# One agent security group per region (hubs need TCP listener on 8444).
resource "aws_security_group" "agent_use1"  {
  provider = aws.use1;  name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

resource "aws_security_group" "agent_usw1" {
  provider = aws.usw1;  name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

resource "aws_security_group" "agent_euc1" {
  provider = aws.euc1;  name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

resource "aws_security_group" "agent_apse1" {
  provider = aws.apse1; name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

resource "aws_security_group" "agent_apne1" {
  provider = aws.apne1; name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

resource "aws_security_group" "agent_sae1" {
  provider = aws.sae1;  name = "nlm-agent"; description = "NLM agent — TCP listener + SSH"
  ingress { from_port = 22;   to_port = 22;   protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  ingress { from_port = 8444; to_port = 8444; protocol = "tcp"; cidr_blocks = ["0.0.0.0/0"] }
  egress  { from_port = 0;    to_port = 0;    protocol = "-1";  cidr_blocks = ["0.0.0.0/0"] }
  tags = { Name = "nlm-agent"; app = "nlm" }
}

# ── S3 bucket for Litestream backup ───────────────────────────────────────────

resource "aws_s3_bucket" "nlm_backup" {
  provider = aws.use1
  bucket   = var.litestream_s3_bucket
  tags     = { app = "nlm"; purpose = "litestream-backup" }
}

resource "aws_s3_bucket_versioning" "nlm_backup" {
  provider = aws.use1
  bucket   = aws_s3_bucket.nlm_backup.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "nlm_backup" {
  provider = aws.use1
  bucket   = aws_s3_bucket.nlm_backup.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}

# IAM user for Litestream S3 access (key credentials written to litestream.env).
resource "aws_iam_user" "litestream" {
  provider = aws.use1
  name     = "nlm-litestream"
  tags     = { app = "nlm" }
}

resource "aws_iam_user_policy" "litestream" {
  provider = aws.use1
  user     = aws_iam_user.litestream.name
  name     = "nlm-litestream-s3"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
      Resource = [
        aws_s3_bucket.nlm_backup.arn,
        "${aws_s3_bucket.nlm_backup.arn}/*",
      ]
    }]
  })
}

resource "aws_iam_access_key" "litestream" {
  provider = aws.use1
  user     = aws_iam_user.litestream.name
}

# ── EC2 instances ──────────────────────────────────────────────────────────────
#
# Controller (us-east-1): runs nlm-controller + Caddy + Litestream
# Hubs: us-east-1, us-west-1, eu-central-1 — nlm-agent hub + listener
# Spokes: ap-southeast-1, ap-northeast-1, sa-east-1 — nlm-agent spoke (timer)

resource "aws_instance" "controller" {
  provider                    = aws.use1
  ami                         = data.aws_ami.ubuntu_use1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_use1.key_name
  vpc_security_group_ids      = [aws_security_group.controller.id]
  associate_public_ip_address = true

  root_block_device {
    volume_size = 20
    volume_type = "gp3"
    encrypted   = true
  }

  user_data = templatefile("${path.module}/user_data.tpl", { role = "controller" })
  tags = { Name = "nlm-controller"; app = "nlm"; role = "controller"; Region = "us-east-1" }
}

resource "aws_eip" "controller" {
  provider = aws.use1
  instance = aws_instance.controller.id
  domain   = "vpc"
  tags     = { Name = "nlm-controller"; app = "nlm" }
}

# ── Route 53 DNS record ────────────────────────────────────────────────────────
# Creates the A record for controller_domain automatically so no manual DNS
# step is required. Route 53 is the default DNS provider for this deployment.

resource "aws_route53_record" "controller" {
  provider = aws.use1
  zone_id  = var.route53_zone_id
  name     = var.controller_domain
  type     = "A"
  ttl      = 60
  records  = [aws_eip.controller.public_ip]
}

resource "aws_instance" "hub_use1" {
  provider                    = aws.use1
  ami                         = data.aws_ami.ubuntu_use1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_use1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_use1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "hub-use1"; app = "nlm"; role = "hub"; Region = "us-east-1" }
}

resource "aws_eip" "hub_use1" {
  provider = aws.use1; instance = aws_instance.hub_use1.id; domain = "vpc"
  tags = { Name = "hub-use1"; app = "nlm" }
}

resource "aws_instance" "hub_usw1" {
  provider                    = aws.usw1
  ami                         = data.aws_ami.ubuntu_usw1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_usw1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_usw1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "hub-usw1"; app = "nlm"; role = "hub"; Region = "us-west-1" }
}

resource "aws_eip" "hub_usw1" {
  provider = aws.usw1; instance = aws_instance.hub_usw1.id; domain = "vpc"
  tags = { Name = "hub-usw1"; app = "nlm" }
}

resource "aws_instance" "hub_euc1" {
  provider                    = aws.euc1
  ami                         = data.aws_ami.ubuntu_euc1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_euc1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_euc1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "hub-euc1"; app = "nlm"; role = "hub"; Region = "eu-central-1" }
}

resource "aws_eip" "hub_euc1" {
  provider = aws.euc1; instance = aws_instance.hub_euc1.id; domain = "vpc"
  tags = { Name = "hub-euc1"; app = "nlm" }
}

resource "aws_instance" "spoke_apse1" {
  provider                    = aws.apse1
  ami                         = data.aws_ami.ubuntu_apse1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_apse1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_apse1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "spoke-apse1"; app = "nlm"; role = "spoke"; Region = "ap-southeast-1" }
}

resource "aws_instance" "spoke_apne1" {
  provider                    = aws.apne1
  ami                         = data.aws_ami.ubuntu_apne1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_apne1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_apne1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "spoke-apne1"; app = "nlm"; role = "spoke"; Region = "ap-northeast-1" }
}

resource "aws_instance" "spoke_sae1" {
  provider                    = aws.sae1
  ami                         = data.aws_ami.ubuntu_sae1.id
  instance_type               = "t3.micro"
  key_name                    = aws_key_pair.nlm_sae1.key_name
  vpc_security_group_ids      = [aws_security_group.agent_sae1.id]
  associate_public_ip_address = true

  root_block_device { volume_size = 10; volume_type = "gp3"; encrypted = true }
  user_data = templatefile("${path.module}/user_data.tpl", { role = "agent" })
  tags = { Name = "spoke-sae1"; app = "nlm"; role = "spoke"; Region = "sa-east-1" }
}
