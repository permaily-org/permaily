# The hourly drift check (ADR 0035): a Lambda function runs
# `tofu plan -detailed-exitcode` of deploy/tofu, as it is on the develop
# branch, against production, and mails the subscribers of the alarms' topic
# (alarms.tf) when the plan is not clean or fails. It reads and never writes:
# its role has no permission that changes anything, the state lock included
# (it plans with -lock=false), and its Cloudflare token is read-only.
#
# Its image (deploy/drift, built and pushed by scripts/drift-image.sh) lives
# in the ECR repository below. The function, its schedule and its alarms exist
# once `drift_image_digest` names an image there (docs/operations.md, "The
# drift check").

variable "drift_image_digest" {
  description = "Digest (sha256:...) of the drift check's image in the ECR repository permaily-drift, from scripts/drift-image.sh. Empty: no function yet."
  type        = string
  default     = ""

  validation {
    condition     = var.drift_image_digest == "" || can(regex("^sha256:[0-9a-f]{64}$", var.drift_image_digest))
    error_message = "drift_image_digest must be empty or sha256: and 64 hexadecimal characters."
  }
}

locals {
  drift      = var.drift_image_digest == "" ? 0 : 1
  drift_name = "permaily-drift"
  drift_arn  = "arn:aws:lambda:${var.aws_region}:${local.account_id}:function:${local.drift_name}"
}

resource "aws_ecr_repository" "drift" {
  name                 = local.drift_name
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "drift" {
  repository = aws_ecr_repository.drift.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the ten newest images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 10 }
      action       = { type = "expire" }
    }]
  })
}

# Lambda pulls the image as the service, for this function only.
resource "aws_ecr_repository_policy" "drift" {
  repository = aws_ecr_repository.drift.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "LambdaPull"
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = ["ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"]
      Condition = { StringLike = { "aws:sourceArn" = local.drift_arn } }
    }]
  })
}

# The Cloudflare token the plan reads the zone with: Zone Read and DNS Read on
# permaily.com, nothing else. The operator puts its value (docs/operations.md),
# so it is in neither the repository nor the state.
resource "aws_secretsmanager_secret" "drift_cloudflare" {
  name        = "permaily/drift/cloudflare-token"
  description = "Read-only Cloudflare API token (Zone Read, DNS Read on permaily.com) for the hourly drift check"
}

data "aws_kms_alias" "tofu_state" {
  name = "alias/permaily-tofu-state"
}

resource "aws_cloudwatch_log_group" "drift" {
  name              = "/aws/lambda/${local.drift_name}"
  retention_in_days = 90
}

resource "aws_iam_role" "drift" {
  name = local.drift_name
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { StringEquals = { "aws:SourceAccount" = local.account_id } }
    }]
  })
}

# What the check does besides reading: its own log, the alarm, its token.
resource "aws_iam_role_policy" "drift_run" {
  name = "run"
  role = aws_iam_role.drift.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "OwnLog"
        Effect   = "Allow"
        Action   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = "${aws_cloudwatch_log_group.drift.arn}:*"
      },
      {
        Sid      = "Alarm"
        Effect   = "Allow"
        Action   = "sns:Publish"
        Resource = aws_sns_topic.sigstore_alarm.arn
      },
      {
        Sid      = "CloudflareToken"
        Effect   = "Allow"
        Action   = "secretsmanager:GetSecretValue"
        Resource = aws_secretsmanager_secret.drift_cloudflare.arn
      },
    ]
  })
}

# Reading the state: its object (never the lock file, which it does not take)
# and the KMS key OpenTofu encrypts it with. kms:GenerateDataKey is there
# because OpenTofu's key provider asks for a data key on every run; a data key
# writes nothing, and the role can put no object anywhere.
resource "aws_iam_role_policy" "drift_state" {
  name = "read-state"
  role = aws_iam_role.drift.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "StateBucket"
        Effect   = "Allow"
        Action   = "s3:ListBucket"
        Resource = "arn:aws:s3:::permaily-tofu-state-${local.account_id}"
      },
      {
        Sid      = "StateObject"
        Effect   = "Allow"
        Action   = "s3:GetObject"
        Resource = "arn:aws:s3:::permaily-tofu-state-${local.account_id}/permaily/terraform.tfstate"
      },
      {
        Sid      = "StateKey"
        Effect   = "Allow"
        Action   = ["kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
        Resource = data.aws_kms_alias.tofu_state.target_key_arn
      },
    ]
  })
}

# Refreshing every resource of deploy/tofu: configuration and metadata, never
# data. No s3:GetObject beyond the state, no DynamoDB item, no SES suppression
# list, no secret but the token above. s3:ListBucket is what HeadBucket needs:
# it lets the role list object keys (identifiers, never contents).
resource "aws_iam_role_policy" "drift_refresh" {
  name = "refresh"
  role = aws_iam_role.drift.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "Describe"
        Effect = "Allow"
        Action = [
          "ec2:Describe*",
          "iam:Get*", "iam:List*",
          "kms:Describe*", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:List*",
          "dynamodb:Describe*", "dynamodb:ListTagsOfResource", "dynamodb:GetResourcePolicy",
          "sns:GetTopicAttributes", "sns:GetDataProtectionPolicy", "sns:ListTagsForResource",
          "ses:GetAccount", "ses:GetEmailIdentity", "ses:GetEmailIdentityPolicies", "ses:ListTagsForResource",
          "amplify:GetApp", "amplify:GetBranch", "amplify:GetDomainAssociation", "amplify:GetWebhook", "amplify:ListTagsForResource",
          "acm:DescribeCertificate", "acm:ListTagsForCertificate",
          "events:Describe*", "events:List*",
          "logs:DescribeLogGroups", "logs:ListTagsForResource", "logs:ListTagsLogGroup",
          "lambda:GetFunction", "lambda:GetFunctionConfiguration", "lambda:GetFunctionCodeSigningConfig",
          "lambda:GetFunctionConcurrency", "lambda:GetPolicy", "lambda:GetRuntimeManagementConfig",
          "lambda:GetFunctionRecursionConfig", "lambda:ListVersionsByFunction", "lambda:ListTags",
          "ecr:DescribeRepositories", "ecr:GetLifecyclePolicy", "ecr:GetRepositoryPolicy", "ecr:ListTagsForResource",
          "cloudwatch:DescribeAlarms", "cloudwatch:ListTagsForResource",
          "secretsmanager:DescribeSecret", "secretsmanager:GetResourcePolicy",
          "s3:ListBucket", "s3:GetBucket*", "s3:GetAccelerateConfiguration", "s3:GetEncryptionConfiguration",
          "s3:GetLifecycleConfiguration", "s3:GetReplicationConfiguration",
        ]
        Resource = "*"
      },
      {
        Sid      = "AmazonLinuxImage"
        Effect   = "Allow"
        Action   = "ssm:GetParameter*"
        Resource = "arn:aws:ssm:${var.aws_region}::parameter/aws/service/ami-amazon-linux-latest/*"
      },
    ]
  })
}

resource "aws_lambda_function" "drift" {
  count = local.drift

  function_name                  = local.drift_name
  description                    = "Hourly tofu plan of deploy/tofu on develop against production; mails when it is not clean. Read-only."
  role                           = aws_iam_role.drift.arn
  package_type                   = "Image"
  image_uri                      = "${aws_ecr_repository.drift.repository_url}@${var.drift_image_digest}"
  architectures                  = ["x86_64"]
  memory_size                    = 2048
  timeout                        = 900
  reserved_concurrent_executions = 1

  # The providers alone take about 1 GB.
  ephemeral_storage {
    size = 4096
  }

  logging_config {
    log_format = "Text"
    log_group  = aws_cloudwatch_log_group.drift.name
  }

  environment {
    variables = {
      DRIFT_REPOSITORY        = "permaily-org/permaily"
      DRIFT_BRANCH            = "develop"
      DRIFT_TOPIC_ARN         = aws_sns_topic.sigstore_alarm.arn
      DRIFT_CLOUDFLARE_SECRET = aws_secretsmanager_secret.drift_cloudflare.arn
    }
  }

  depends_on = [aws_iam_role_policy.drift_run, aws_ecr_repository_policy.drift]
}

# Once an hour, at minute 23: away from verify-production's 7 and 37.
resource "aws_cloudwatch_event_rule" "drift" {
  count = local.drift

  name                = local.drift_name
  description         = "Starts the hourly drift check"
  schedule_expression = "cron(23 * * * ? *)"
}

resource "aws_cloudwatch_event_target" "drift" {
  count = local.drift

  rule = aws_cloudwatch_event_rule.drift[0].name
  arn  = aws_lambda_function.drift[0].arn

  retry_policy {
    maximum_event_age_in_seconds = 600
    maximum_retry_attempts       = 0
  }
}

resource "aws_lambda_permission" "drift" {
  count = local.drift

  statement_id  = "hourly"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.drift[0].function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.drift[0].arn
}

# The check mails what it finds itself. These mail when it cannot: it failed
# without mailing (a crash, a timeout, SNS refused), or it stopped running.
resource "aws_cloudwatch_metric_alarm" "drift_errors" {
  count = local.drift

  alarm_name          = "permaily-drift-errors"
  alarm_description   = "The hourly drift check failed without mailing its result (docs/operations.md, \"The drift check\")."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.drift[0].function_name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.sigstore_alarm.arn]
  ok_actions          = [aws_sns_topic.sigstore_alarm.arn]
}

resource "aws_cloudwatch_metric_alarm" "drift_stopped" {
  count = local.drift

  alarm_name          = "permaily-drift-stopped"
  alarm_description   = "The hourly drift check did not run for three hours (docs/operations.md, \"The drift check\")."
  namespace           = "AWS/Lambda"
  metric_name         = "Invocations"
  dimensions          = { FunctionName = aws_lambda_function.drift[0].function_name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.sigstore_alarm.arn]
  ok_actions          = [aws_sns_topic.sigstore_alarm.arn]
}

output "drift_repository" {
  description = "ECR repository of the drift check's image (scripts/drift-image.sh pushes there)."
  value       = aws_ecr_repository.drift.repository_url
}
