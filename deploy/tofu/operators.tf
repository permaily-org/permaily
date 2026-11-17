# Operators other than the account's owner (ADR 0036): IAM users in the group
# permaily-operators, which can deploy and operate and nothing more, and only
# after signing in with MFA. No credential is made here: each operator sets
# their console password (from a one-time password the owner gives) and their
# MFA device at first sign-in, and makes an access key only if they need the
# CLI (docs/operations.md, "Operators"). So nothing secret is in the state.
#
# What an operator cannot do, on purpose: create, delete or change a KMS key
# or its policy (so no `tofu apply` of release_keys, and no deleting an old
# release's key), anything in IAM beyond their own password, MFA and access
# keys, write the OpenTofu state (so no apply at all), or touch DynamoDB.

variable "operators" {
  description = "Operators by short name; each is the IAM user permaily-operator-<name> in the group permaily-operators. Removing a name deletes the user with their password, MFA devices and access keys."
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for n in var.operators : can(regex("^[a-z][a-z0-9-]{1,30}$", n))])
    error_message = "Each operator's name must be lowercase letters, digits and dashes."
  }
}

resource "aws_iam_group" "operators" {
  name = "permaily-operators"
}

resource "aws_iam_user" "operator" {
  for_each = var.operators

  name          = "permaily-operator-${each.key}"
  force_destroy = true # removing an operator removes their password, MFA devices and keys
}

resource "aws_iam_user_group_membership" "operator" {
  for_each = var.operators

  user   = aws_iam_user.operator[each.key].name
  groups = [aws_iam_group.operators.name]
}

data "aws_kms_alias" "operators_state" {
  name = "alias/permaily-tofu-state"
}

locals {
  operator_self = "arn:aws:iam::${local.account_id}:user/$${aws:username}"
}

# Without MFA an operator can only set their password and their MFA device.
# With MFA, also their own access keys. This is AWS's documented policy for
# users who manage their own MFA.
resource "aws_iam_group_policy" "operators_mfa" {
  name  = "require-mfa"
  group = aws_iam_group.operators.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "OwnPasswordAndMFA"
        Effect = "Allow"
        Action = [
          "iam:GetAccountPasswordPolicy", "iam:ListVirtualMFADevices",
        ]
        Resource = "*"
      },
      {
        Sid      = "OwnUser"
        Effect   = "Allow"
        Action   = ["iam:GetUser", "iam:ChangePassword", "iam:ListMFADevices", "iam:EnableMFADevice", "iam:ResyncMFADevice", "iam:DeactivateMFADevice", "iam:GetMFADevice", "iam:ListAccessKeys", "iam:CreateAccessKey", "iam:UpdateAccessKey", "iam:DeleteAccessKey", "iam:GetAccessKeyLastUsed"]
        Resource = local.operator_self
      },
      {
        Sid      = "OwnVirtualMFADevice"
        Effect   = "Allow"
        Action   = ["iam:CreateVirtualMFADevice", "iam:DeleteVirtualMFADevice"]
        Resource = "arn:aws:iam::${local.account_id}:mfa/*"
      },
      {
        Sid    = "NothingElseWithoutMFA"
        Effect = "Deny"
        NotAction = [
          "iam:CreateVirtualMFADevice", "iam:EnableMFADevice", "iam:GetUser", "iam:GetMFADevice", "iam:ListMFADevices",
          "iam:ListVirtualMFADevices", "iam:ResyncMFADevice", "iam:ChangePassword", "iam:GetAccountPasswordPolicy",
          "sts:GetSessionToken",
        ]
        Resource  = "*"
        Condition = { BoolIfExists = { "aws:MultiFactorAuthPresent" = "false" } }
      },
    ]
  })
}

# What deploying (scripts/deploy.sh) and operating (docs/operations.md) need.
resource "aws_iam_group_policy" "operators_operate" {
  name  = "operate"
  group = aws_iam_group.operators.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ReadTheStateForOutputs"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = "arn:aws:s3:::permaily-tofu-state-${local.account_id}"
      },
      {
        Sid      = "ReadTheStateObject"
        Effect   = "Allow"
        Action   = "s3:GetObject"
        Resource = "arn:aws:s3:::permaily-tofu-state-${local.account_id}/permaily/terraform.tfstate"
      },
      {
        Sid      = "DecryptTheState"
        Effect   = "Allow"
        Action   = ["kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
        Resource = data.aws_kms_alias.operators_state.target_key_arn
      },
      {
        Sid      = "UploadArtifacts"
        Effect   = "Allow"
        Action   = ["s3:PutObject", "s3:GetObject"]
        Resource = "${aws_s3_bucket.artifacts.arn}/artifacts/*"
      },
      {
        Sid      = "ListArtifacts"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = aws_s3_bucket.artifacts.arn
      },
      {
        Sid      = "RunOnTheInstance"
        Effect   = "Allow"
        Action   = "ssm:SendCommand"
        Resource = [aws_instance.enclave_host.arn, "arn:aws:ssm:${var.aws_region}::document/AWS-RunShellScript"]
      },
      {
        Sid      = "SessionOnTheInstance"
        Effect   = "Allow"
        Action   = "ssm:StartSession"
        Resource = [aws_instance.enclave_host.arn, "arn:aws:ssm:${var.aws_region}:${local.account_id}:document/SSM-SessionManagerRunShell"]
      },
      {
        Sid      = "OwnSessions"
        Effect   = "Allow"
        Action   = ["ssm:TerminateSession", "ssm:ResumeSession"]
        Resource = "arn:aws:ssm:${var.aws_region}:${local.account_id}:session/$${aws:username}-*"
      },
      {
        Sid    = "SeeCommandsAndSessions"
        Effect = "Allow"
        Action = [
          "ssm:GetCommandInvocation", "ssm:ListCommandInvocations", "ssm:ListCommands", "ssm:CancelCommand",
          "ssm:DescribeInstanceInformation", "ssm:DescribeSessions", "ssm:GetConnectionStatus",
        ]
        Resource = "*"
      },
      {
        Sid    = "ReadLogs"
        Effect = "Allow"
        Action = ["logs:FilterLogEvents", "logs:GetLogEvents", "logs:DescribeLogStreams", "logs:StartLiveTail"]
        Resource = [
          "arn:aws:logs:${var.aws_region}:${local.account_id}:log-group:permaily-host:*",
          "arn:aws:logs:${var.aws_region}:${local.account_id}:log-group:/aws/lambda/permaily-drift:*",
        ]
      },
      {
        Sid      = "RunTheDriftCheck"
        Effect   = "Allow"
        Action   = "lambda:InvokeFunction"
        Resource = "arn:aws:lambda:${var.aws_region}:${local.account_id}:function:permaily-drift"
      },
      {
        Sid      = "ListAnchorVersions"
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:ListBucketVersions"]
        Resource = aws_s3_bucket.anchors.arn
      },
      {
        Sid    = "SeeHowThingsAre"
        Effect = "Allow"
        Action = [
          "ec2:DescribeInstances", "ec2:DescribeInstanceStatus",
          "kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListAliases", "kms:ListKeys",
          "kms:ListResourceTags", "kms:ListGrants",
          "logs:DescribeLogGroups",
          "cloudwatch:DescribeAlarms", "cloudwatch:GetMetricData", "cloudwatch:GetMetricStatistics", "cloudwatch:ListMetrics",
          "events:DescribeRule", "events:ListRules", "events:ListTargetsByRule",
          "ses:GetAccount", "ses:GetEmailIdentity",
          "cloudtrail:LookupEvents",
        ]
        Resource = "*"
      },
      {
        Sid      = "SuppressedAddresses"
        Effect   = "Allow"
        Action   = ["ses:GetSuppressedDestination", "ses:DeleteSuppressedDestination"]
        Resource = "*"
      },
    ]
  })
}

# Holds whatever else is ever attached to an operator: no key or key policy
# changed, no IAM but their own password, MFA and keys, no state written.
resource "aws_iam_group_policy" "operators_guard" {
  name  = "guard"
  group = aws_iam_group.operators.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "NoKeyChanges"
        Effect = "Deny"
        Action = [
          "kms:CreateKey", "kms:PutKeyPolicy", "kms:ScheduleKeyDeletion", "kms:CancelKeyDeletion", "kms:CreateGrant",
          "kms:RetireGrant", "kms:RevokeGrant", "kms:EnableKey", "kms:DisableKey", "kms:CreateAlias", "kms:UpdateAlias",
          "kms:DeleteAlias", "kms:ReplicateKey", "kms:UpdateKeyDescription", "kms:UpdatePrimaryRegion",
          "kms:EnableKeyRotation", "kms:DisableKeyRotation", "kms:TagResource", "kms:UntagResource",
        ]
        Resource = "*"
      },
      {
        Sid    = "NoIAMButTheirOwn"
        Effect = "Deny"
        Action = [
          "iam:Add*", "iam:Attach*", "iam:Change*", "iam:Create*", "iam:Deactivate*", "iam:Delete*", "iam:Detach*",
          "iam:Enable*", "iam:PassRole", "iam:Put*", "iam:Remove*", "iam:Resync*", "iam:Set*", "iam:Tag*",
          "iam:Untag*", "iam:Update*", "iam:Upload*",
        ]
        NotResource = [local.operator_self, "arn:aws:iam::${local.account_id}:mfa/*"]
      },
      {
        Sid      = "NoStateWrites"
        Effect   = "Deny"
        Action   = ["s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:PutBucket*", "s3:PutLifecycleConfiguration"]
        Resource = ["arn:aws:s3:::permaily-tofu-state-${local.account_id}", "arn:aws:s3:::permaily-tofu-state-${local.account_id}/*"]
      },
      {
        Sid      = "NoTable"
        Effect   = "Deny"
        Action   = "dynamodb:*"
        Resource = "*"
      },
    ]
  })
}

output "operators" {
  description = "The operators' IAM users."
  value       = [for u in aws_iam_user.operator : u.name]
}
