# Where the OpenTofu state of ../ lives: a versioned S3 bucket and the KMS key
# OpenTofu encrypts the state with before uploading it (ADR 0014). Applied once.
# This root keeps its own state in this directory, committed: it holds no secrets.

terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "aws_profile" {
  description = "AWS CLI profile. Empty uses credentials from the environment."
  type        = string
  default     = "permaily"
}

provider "aws" {
  profile = var.aws_profile != "" ? var.aws_profile : null
  region  = "us-east-2"
  default_tags {
    tags = { Project = "permaily", ManagedBy = "opentofu" }
  }
}

data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
}

resource "aws_s3_bucket" "state" {
  bucket = "permaily-tofu-state-${local.account_id}"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Old versions are the backup of the state; 90 days of them is enough.
resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "expire-old-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = 90
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "TLSOnly"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.state.arn, "${aws_s3_bucket.state.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

# Operators use this key through IAM. It is separate from the mail key, which
# nobody outside the enclave may use.
resource "aws_kms_key" "state" {
  description             = "Encrypts the OpenTofu state of Permaily (used by operators)"
  deletion_window_in_days = 30
  enable_key_rotation     = true
}

resource "aws_kms_alias" "state" {
  name          = "alias/permaily-tofu-state"
  target_key_id = aws_kms_key.state.key_id
}

output "bucket" {
  value = aws_s3_bucket.state.bucket
}

output "kms_key_arn" {
  value = aws_kms_key.state.arn
}
