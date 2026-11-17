# Operators other than the account's owner (ADR 0036). They sign in through
# the organization's IAM Identity Center, which enforces MFA at sign-in, with a
# permission set that references the two customer-managed policies below by
# name and path (docs/operations.md, "Operators"). The organization's service
# control policy denies creating IAM groups and console passwords for IAM users
# in this account, so there is no IAM user, group or attachment here: the
# permission set, its assignment and its role are made in the organization's
# management account, and no credential of an operator is in this state.
#
# What an operator cannot do, on purpose: create, delete or change a KMS key
# or its policy (so no `tofu apply` of release_keys, and no deleting an old
# release's key), change anything in IAM, write the OpenTofu state (so no apply
# at all), delete or reconfigure the anchors' Object Lock buckets, or touch
# DynamoDB.

data "aws_kms_alias" "operators_state" {
  name = "alias/permaily-tofu-state"
}

# The two policies are JSON templates in operators/, so that
# scripts/operator-policies.test.ts evaluates them as IAM would: the guard's
# denies hold against anything else the permission set carries (another
# managed policy, an inline policy), and weakening any of them fails the test.
# They are written for a role session: no aws:username (a role session has
# none) and no aws:MultiFactorAuthPresent (IAM Identity Center checks MFA at
# sign-in and does not set it in the role's sessions).
locals {
  operator_policy_vars = {
    account_id          = local.account_id
    region              = var.aws_region
    state_key_arn       = data.aws_kms_alias.operators_state.target_key_arn
    artifacts_arn       = aws_s3_bucket.artifacts.arn
    anchors_arn         = aws_s3_bucket.anchors.arn
    anchors_staging_arn = aws_s3_bucket.anchors_staging.arn
    instance_arn        = aws_instance.enclave_host.arn
  }
}

# Holds whatever else the permission set ever carries: no key or key policy
# changed, nothing in IAM changed, no state written, the anchors' buckets kept,
# no DynamoDB. The permission set references it as permaily-operators-guard,
# path /.
resource "aws_iam_policy" "operators_guard" {
  name        = "permaily-operators-guard"
  path        = "/"
  description = "Operators (ADR 0036): no KMS key change, no IAM change, no state write, no anchors deleted, no DynamoDB."
  policy      = jsonencode(jsondecode(templatefile("${path.module}/operators/guard.json.tftpl", local.operator_policy_vars)))
}

# What deploying (scripts/deploy.sh) and operating (docs/operations.md) need.
# The permission set references it as permaily-operators-operate, path /.
resource "aws_iam_policy" "operators_operate" {
  name        = "permaily-operators-operate"
  path        = "/"
  description = "Operators (ADR 0036): what deploying and operating need."
  policy      = jsonencode(jsondecode(templatefile("${path.module}/operators/operate.json.tftpl", local.operator_policy_vars)))
}

# The IAM user that the first apply of ADR 0036 made before the service
# control policy stopped it, with no password, key, MFA device or group. It is
# forgotten rather than destroyed: it was made with force_destroy, and
# destroying it then deletes its login profile, which that policy denies. The
# owner deletes it by hand after the apply (docs/operations.md, "Operators").
# This block can go once that apply is done.
removed {
  from = aws_iam_user.operator

  lifecycle {
    destroy = false
  }
}
