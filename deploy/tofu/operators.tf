# Operators other than the account's owner (ADR 0036): IAM users in the group
# permaily-operators, which can deploy and operate and nothing more, and only
# after signing in with MFA. No credential is made here: each operator sets
# their console password (from a one-time password the owner gives) and, while
# the owner holds their enrollment open (operators_enrolling), their first MFA
# device; they make an access key only once it is closed, and only if they
# need the CLI (docs/operations.md, "Operators"). So nothing secret is in the
# state, and a stolen access key cannot enroll a device of its own: outside
# that window, adding a device takes a session already signed in with MFA.
#
# What an operator cannot do, on purpose: create, delete or change a KMS key
# or its policy (so no `tofu apply` of release_keys, and no deleting an old
# release's key), anything in IAM beyond their own password, MFA and access
# keys, write the OpenTofu state (so no apply at all), or touch DynamoDB.

variable "operators" {
  description = "Operators by a generic label, never a person's name (the repository is public): \"2\" is the IAM user permaily-operator-2 in the group permaily-operators. Removing a label deletes the user with their password, MFA devices and access keys."
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for n in var.operators : can(regex("^[a-z0-9][a-z0-9-]{0,30}$", n))])
    error_message = "Each operator's label must be lowercase letters, digits and dashes."
  }
}

variable "operators_enrolling" {
  description = "Operators whose first MFA device may be added without MFA, during onboarding or after a lost device, and who cannot make an access key meanwhile. Empty once each has signed in with MFA (docs/operations.md, \"Operators\")."
  type        = set(string)
  default     = []

  validation {
    condition     = alltrue([for n in var.operators_enrolling : contains(var.operators, n)])
    error_message = "Each operator in operators_enrolling must be in operators."
  }
}

resource "aws_iam_group" "operators" {
  name = "permaily-operators"
}

resource "aws_iam_user" "operator" {
  for_each = var.operators

  name          = "permaily-operator-${each.key}"
  force_destroy = true # removing an operator removes their password, MFA devices and keys

  # The tag opens the enrollment of a first MFA device (operators_mfa). The
  # operator cannot set it: the guard denies tagging themselves.
  tags = contains(var.operators_enrolling, each.key) ? { (local.mfa_enrollment_tag) = "open" } : {}
}

resource "aws_iam_user_group_membership" "operator" {
  for_each = var.operators

  user   = aws_iam_user.operator[each.key].name
  groups = [aws_iam_group.operators.name]
}

data "aws_kms_alias" "operators_state" {
  name = "alias/permaily-tofu-state"
}

# The three policies are JSON templates in operators/, so that
# scripts/operator-policies.test.ts evaluates them as IAM would: a long-term
# key without MFA can enroll no device and do nothing, an open enrollment lets
# the first device in and no access key, and the guard holds. Each operator's
# one virtual MFA device is named after their user, mfa/${aws:username}.
locals {
  mfa_enrollment_tag = "permaily-mfa-enrollment"
  operator_policy_vars = {
    account_id         = local.account_id
    region             = var.aws_region
    mfa_enrollment_tag = local.mfa_enrollment_tag
    state_key_arn      = data.aws_kms_alias.operators_state.target_key_arn
    artifacts_arn      = aws_s3_bucket.artifacts.arn
    anchors_arn        = aws_s3_bucket.anchors.arn
    instance_arn       = aws_instance.enclave_host.arn
  }
}

# Without MFA an operator can only see their user and set their password, and,
# while their enrollment is open (operators_enrolling), add their MFA device.
# With MFA, also more MFA devices and their own access keys. AWS's documented
# policy for users who manage their own MFA lets anyone without MFA add a
# device, so a stolen access key could make its own second factor; here that
# takes the owner opening the enrollment, which also refuses access keys.
resource "aws_iam_group_policy" "operators_mfa" {
  name   = "require-mfa"
  group  = aws_iam_group.operators.name
  policy = jsonencode(jsondecode(templatefile("${path.module}/operators/require-mfa.json.tftpl", local.operator_policy_vars)))
}

# What deploying (scripts/deploy.sh) and operating (docs/operations.md) need.
resource "aws_iam_group_policy" "operators_operate" {
  name   = "operate"
  group  = aws_iam_group.operators.name
  policy = jsonencode(jsondecode(templatefile("${path.module}/operators/operate.json.tftpl", local.operator_policy_vars)))
}

# Holds whatever else is ever attached to an operator: no key or key policy
# changed, no IAM but their own password, MFA and keys (not their tags, groups
# or policies), no state written.
resource "aws_iam_group_policy" "operators_guard" {
  name   = "guard"
  group  = aws_iam_group.operators.name
  policy = jsonencode(jsondecode(templatefile("${path.module}/operators/guard.json.tftpl", local.operator_policy_vars)))
}

output "operators" {
  description = "The operators' IAM users."
  value       = [for u in aws_iam_user.operator : u.name]
}
