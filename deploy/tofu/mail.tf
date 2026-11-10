# F1: mail storage and keys. The enclave stores only sealed data here; the
# mailbox data key is wrapped by this KMS key, which releases it only to an
# attested enclave whose PCR0 is in var.enclave_pcr0_allowlist.

variable "enclave_pcr0_allowlist" {
  description = "PCR0 values (hex) of released enclave images allowed to use the mail key. Add a release's PCR0 before deploying it."
  type        = list(string)
  default     = []
}

variable "mail_dns_enabled" {
  description = "Publish the MX, MTA-STS and TLS-RPT records. Keep false until the enclave serves SMTP."
  type        = bool
  default     = false
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  # One S3 lifecycle rule per supported retention period (internal/store.RetentionDays).
  retention_days = [7, 30, 90, 180, 365]
}

resource "aws_kms_key" "mail" {
  description             = "Permaily mailbox data keys (released only to attested enclaves)"
  key_usage               = "ENCRYPT_DECRYPT"
  deletion_window_in_days = 30
  enable_key_rotation     = true

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{
        # Administration only: no principal in the account can use the key to
        # decrypt or generate data keys. Changing this policy is visible in
        # CloudTrail and in the enclave's /status, which permaily-verify checks
        # (pkg/kmspolicy). Keep every action here in that package's safe list.
        Sid       = "KeyAdministration"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${local.account_id}:root" }
        Action = [
          "kms:Describe*", "kms:Get*", "kms:List*", "kms:PutKeyPolicy",
          "kms:EnableKeyRotation", "kms:DisableKeyRotation", "kms:TagResource", "kms:UntagResource",
          "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion", "kms:UpdateKeyDescription",
          "kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias",
        ]
        Resource = "*"
        }, {
        # The enclave reads this policy and the grants on the key and serves
        # them on /status over its attested TLS key, so anyone can check which
        # images may use the key. Neither action reveals key material.
        Sid       = "EnclaveReadsPolicy"
        Effect    = "Allow"
        Principal = { AWS = aws_iam_role.enclave_host.arn }
        Action    = ["kms:GetKeyPolicy", "kms:ListGrants"]
        Resource  = "*"
      }],
      length(var.enclave_pcr0_allowlist) == 0 ? [] : [{
        # Only the enclave, proving its image (PCR0) with an attestation
        # document in the Recipient parameter, gets data keys. A request
        # without Recipient has no attestation and is denied.
        Sid       = "EnclaveDataKeys"
        Effect    = "Allow"
        Principal = { AWS = aws_iam_role.enclave_host.arn }
        Action    = ["kms:GenerateDataKey", "kms:Decrypt"]
        Resource  = "*"
        Condition = {
          StringEqualsIgnoreCase = {
            "kms:RecipientAttestation:ImageSha384" = var.enclave_pcr0_allowlist
          }
        }
      }]
    )
  })
}

resource "aws_kms_alias" "mail" {
  name          = "alias/permaily-mail"
  target_key_id = aws_kms_key.mail.key_id
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
# host) for storage. They cannot decrypt anything without an attested enclave.
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
      {
        Effect   = "Allow"
        Action   = ["kms:GenerateDataKey", "kms:Decrypt"]
        Resource = aws_kms_key.mail.arn
      },
    ]
  })
}

# --- DNS for the inbox (published once the enclave serves SMTP) ---

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
  content = "\"v=STSv1; id=20261109\""
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

output "mail_kms_key_arn" {
  value = aws_kms_key.mail.arn
}

output "mail_table" {
  value = aws_dynamodb_table.mail.name
}

output "mail_bucket" {
  value = aws_s3_bucket.mail.bucket
}
