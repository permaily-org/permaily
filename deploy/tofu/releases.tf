# One KMS key per enclave image (ADR 0013). It holds the root secret for that
# image, and its policy is written once and can never be changed: it gives
# nobody kms:PutKeyPolicy or kms:CreateGrant, the account's root user included
# (bypass_policy_lockout_safety_check lets such a policy be created).
#
#   - Only an enclave that proves this image (PCR0) can decrypt.
#   - Anyone in the account can create a data key under it: the running enclave
#     must, to hand the root secret to the next release. What is stored under
#     the key is trusted for the attestation document stored with it, never
#     because it decrypts, so this gives the operator nothing.
#   - The account can read the key and its policy, and schedule its deletion.
#
# permaily-verify checks exactly this on the live key (kmspolicy.Sealed), and
# so does every enclave before it uses a key. Keep the policy inside what that
# function accepts.
#
# A key cannot be edited after it is created: to change anything, create
# another key. Deleting the key of the image that runs loses the root secret.

variable "release_keys" {
  description = "PCR0 (hex) of each enclave image that has a KMS key, by a label: the release tag, or a name for a staging image. Add an image before handing it the root secret; remove it to schedule the deletion of its key once a later release has run for a while."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for pcr0 in values(var.release_keys) : can(regex("^[0-9a-f]{96}$", pcr0))])
    error_message = "Each value must be a PCR0: 96 lowercase hexadecimal characters."
  }
}

resource "aws_kms_key" "release" {
  for_each = var.release_keys

  description                        = "Permaily root secret, sealed to the enclave image of ${each.key}"
  key_usage                          = "ENCRYPT_DECRYPT"
  deletion_window_in_days            = 7
  bypass_policy_lockout_safety_check = true

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "ReadAndDelete"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${local.account_id}:root" }
        Action = [
          "kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListGrants",
          "kms:ListKeyPolicies", "kms:ListResourceTags", "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion",
          "kms:CreateAlias", "kms:DeleteAlias",
        ]
        Resource = "*"
      },
      {
        Sid       = "AnyoneLocks"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${local.account_id}:root" }
        Action    = "kms:GenerateDataKey"
        Resource  = "*"
      },
      {
        Sid       = "OnlyThisImageOpens"
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${local.account_id}:root" }
        Action    = "kms:Decrypt"
        Resource  = "*"
        Condition = {
          StringEqualsIgnoreCase = {
            "kms:RecipientAttestation:ImageSha384" = each.value
          }
        }
      },
    ]
  })

  lifecycle {
    # The policy cannot be changed in place, and a new key would not hold the
    # root secret. Any change here must fail instead of replacing the key.
    ignore_changes = [policy, description]
  }
}

# How an enclave finds the key of an image: by the image's PCR0. The alias
# only locates the key; nothing trusts a key for its alias.
resource "aws_kms_alias" "release" {
  for_each      = var.release_keys
  name          = "alias/permaily-release-${each.value}"
  target_key_id = aws_kms_key.release[each.key].key_id
}

# The instance role may ask; each key's policy decides. Without an attestation
# document of the right image, Decrypt is denied whoever holds these credentials.
resource "aws_iam_role_policy" "release_keys" {
  name = "release-keys"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["kms:DescribeKey", "kms:GetKeyPolicy", "kms:ListGrants", "kms:GenerateDataKey", "kms:Decrypt"]
      Resource = "arn:aws:kms:${var.aws_region}:${local.account_id}:key/*"
    }]
  })
}

output "release_keys" {
  description = "KMS key of each image in var.release_keys."
  value       = { for label, key in aws_kms_key.release : label => key.arn }
}
