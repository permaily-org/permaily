# F1: mail storage. The enclave stores only sealed data here, under a root
# secret that only enclaves hold (ADR 0013, releases.tf).
#
# Releases up to v0.6.0 wrapped data keys directly with a KMS key that lived
# here (alias/permaily-mail). v0.7.0 moved them under the root secret, the key
# was retired, and it was scheduled for deletion on 2026-11-10.

variable "mail_dns_enabled" {
  description = "Publish the MX, MTA-STS and TLS-RPT records. Set false only for a deployment that must not receive mail."
  type        = bool
  default     = true
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  # One S3 lifecycle rule per supported retention period (internal/store.RetentionDays).
  retention_days = [7, 30, 90, 180, 365]
}

resource "aws_dynamodb_table" "mail" {
  name         = "permaily-mail"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  deletion_protection_enabled = true
}

resource "aws_s3_bucket" "mail" {
  bucket = "permaily-mail-${local.account_id}"
}

resource "aws_s3_bucket_public_access_block" "mail" {
  bucket                  = aws_s3_bucket.mail.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "mail" {
  bucket = aws_s3_bucket.mail.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "mail" {
  bucket = aws_s3_bucket.mail.id

  dynamic "rule" {
    for_each = local.retention_days
    content {
      id     = "retention-${rule.value}-days"
      status = "Enabled"
      filter {
        tag {
          key   = "retention-days"
          value = tostring(rule.value)
        }
      }
      # One extra day so the metadata (DynamoDB TTL) always expires first.
      expiration {
        days = rule.value + 1
      }
    }
  }

  rule {
    id     = "abort-incomplete-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

# The parent instance's role: the enclave uses these credentials (through the
# host) for storage. Everything stored is sealed; the keys are in releases.tf.
resource "aws_iam_role_policy" "mail" {
  name = "mail-storage"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:DeleteItem", "dynamodb:Query"]
        Resource = aws_dynamodb_table.mail.arn
      },
      {
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:PutObjectTagging", "s3:DeleteObject"]
        Resource = "${aws_s3_bucket.mail.arn}/raw/*"
      },
    ]
  })
}

# --- DNS for the inbox. permaily-verify checks the MX and the MTA-STS record. ---

resource "cloudflare_dns_record" "inbox_mx" {
  count    = var.mail_dns_enabled ? 1 : 0
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = "inbox.${var.domain}"
  type     = "MX"
  content  = "inbox.${var.domain}"
  priority = 10
  ttl      = 300
  comment  = "Permaily enclave MX (managed by OpenTofu)"
}

resource "cloudflare_dns_record" "mta_sts" {
  count   = var.mail_dns_enabled ? 1 : 0
  zone_id = data.cloudflare_zone.main.zone_id
  name    = "_mta-sts.inbox.${var.domain}"
  type    = "TXT"
  # Change the id whenever the policy served by the enclave changes.
  content = "\"v=STSv1; id=20261110\""
  ttl     = 300
  comment = "MTA-STS for the enclave MX (managed by OpenTofu)"
}

resource "cloudflare_dns_record" "tls_rpt" {
  count   = var.mail_dns_enabled ? 1 : 0
  zone_id = data.cloudflare_zone.main.zone_id
  name    = "_smtp._tls.inbox.${var.domain}"
  type    = "TXT"
  content = "\"v=TLSRPTv1; rua=mailto:${jsondecode(file("${path.module}/../enclave.json")).mailbox_address}\""
  ttl     = 300
  comment = "SMTP TLS reports, delivered to the test mailbox (managed by OpenTofu)"
}

output "mail_table" {
  value = aws_dynamodb_table.mail.name
}

output "mail_bucket" {
  value = aws_s3_bucket.mail.bucket
}
