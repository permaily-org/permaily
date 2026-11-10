terraform {
  required_version = ">= 1.8"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  # Empty profile means "use credentials from the environment" (e.g. exported
  # from an `aws login` session with `aws configure export-credentials`).
  profile = var.aws_profile != "" ? var.aws_profile : null
  region  = var.aws_region
  default_tags {
    tags = { Project = "permaily", ManagedBy = "opentofu" }
  }
}

# Only for the public site's certificate: Amplify serves through CloudFront,
# which takes certificates from us-east-1 and nowhere else.
provider "aws" {
  alias   = "us_east_1"
  profile = var.aws_profile != "" ? var.aws_profile : null
  region  = "us-east-1"
  default_tags {
    tags = { Project = "permaily", ManagedBy = "opentofu" }
  }
}

# Reads the API token from CLOUDFLARE_API_TOKEN. Scope it to DNS edit on the zone.
provider "cloudflare" {}

data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

# --- Network: one public subnet, only 443 open. Access is through SSM, no SSH.

resource "aws_vpc" "main" {
  cidr_block           = "10.42.0.0/16"
  enable_dns_hostnames = true
  tags                 = { Name = "permaily" }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "10.42.1.0/24"
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = false
  tags                    = { Name = "permaily-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "enclave_host" {
  name        = "permaily-enclave-host"
  description = "HTTPS to the enclave; TLS terminates inside the enclave"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "HTTPS (TLS terminated in the enclave, incl. ACME TLS-ALPN-01)"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Declared here, not as a separate rule resource: a security group with
  # inline rules removes any rule it does not list on the next apply.
  ingress {
    description = "SMTP to the enclave MX (STARTTLS terminates in the enclave)"
    from_port   = 25
    to_port     = 25
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "Package installs, SSM, S3 and allowlisted enclave egress"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# --- Artifacts: deploy.sh uploads the EIF and permaily-host here; the instance pulls them.

resource "aws_s3_bucket" "artifacts" {
  bucket        = "permaily-artifacts-${data.aws_caller_identity.current.account_id}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket                  = aws_s3_bucket.artifacts.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# --- Instance role: SSM for administration, read-only access to artifacts.

resource "aws_iam_role" "enclave_host" {
  name = "permaily-enclave-host"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.enclave_host.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "artifacts_read" {
  name = "artifacts-read"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["s3:GetObject"]
      Resource = "${aws_s3_bucket.artifacts.arn}/*"
    }]
  })
}

resource "aws_iam_instance_profile" "enclave_host" {
  name = "permaily-enclave-host"
  role = aws_iam_role.enclave_host.name
}

# --- The parent instance (untrusted) with Nitro Enclaves enabled.

resource "aws_instance" "enclave_host" {
  ami                    = data.aws_ssm_parameter.al2023_arm64.insecure_value
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.enclave_host.id]
  iam_instance_profile   = aws_iam_instance_profile.enclave_host.name

  enclave_options {
    enabled = true
  }

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 16
    encrypted   = true
  }

  user_data = templatefile("${path.module}/../host/cloud-init.sh.tftpl", {
    enclave_memory_mib = var.enclave_memory_mib
    enclave_cpus       = var.enclave_cpus
    host_unit          = file("${path.module}/../host/permaily-host.service")
    enclave_unit       = file("${path.module}/../host/permaily-enclave.service")
  })
  user_data_replace_on_change = true

  lifecycle {
    ignore_changes = [ami]
  }

  tags = { Name = "permaily-enclave-host" }
}

resource "aws_eip" "enclave_host" {
  domain   = "vpc"
  instance = aws_instance.enclave_host.id
  tags     = { Name = "permaily-enclave-host" }
}

# --- DNS in Cloudflare. proxied = false is mandatory: if Cloudflare proxied
# these names it would terminate TLS and verification would (correctly) fail.

data "cloudflare_zone" "main" {
  filter = { name = var.domain }
}

resource "cloudflare_dns_record" "enclave_a" {
  for_each = toset(var.enclave_subdomains)
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = "${each.key}.${var.domain}"
  type     = "A"
  content  = aws_eip.enclave_host.public_ip
  proxied  = false
  ttl      = 300
  comment  = "Permaily enclave (managed by OpenTofu; must stay DNS-only)"
}

# Only the enclave's own ACME account may issue for the enclave names, and only
# with TLS-ALPN-01, which is answered from inside the enclave. A subdomain's own
# CAA record takes precedence over the apex's (which allows Amazon for the site).
# permaily-verify checks these records against the account the enclave reports.
locals {
  enclave_caa_values = length(var.acme_account_uris) == 0 ? ["letsencrypt.org"] : [
    for uri in var.acme_account_uris : "letsencrypt.org; accounturi=${uri}; validationmethods=tls-alpn-01"
  ]
  enclave_caa = {
    for pair in setproduct(var.enclave_subdomains, range(length(local.enclave_caa_values))) :
    "${pair[0]}/${pair[1]}" => { name = pair[0], value = local.enclave_caa_values[pair[1]] }
  }
}

resource "cloudflare_dns_record" "enclave_caa" {
  for_each = local.enclave_caa
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = "${each.value.name}.${var.domain}"
  type     = "CAA"
  ttl      = 300
  data = {
    flags = 0
    tag   = "issue"
    value = each.value.value
  }
  comment = "Permaily enclave (managed by OpenTofu)"
}
