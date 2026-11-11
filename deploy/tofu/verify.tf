# Starts the verify-production workflow every half hour. GitHub's own schedule
# starts it a few times a day at most (docs/operations.md), so EventBridge calls
# GitHub's API instead; the workflow keeps its schedule as a second trigger that
# the operator of this account does not time.
#
# This only says "run now": the check itself runs on GitHub, with the code of
# the repository, and its result is public there. Nothing here is inside the
# enclave or on the instance.

variable "github_dispatch_token" {
  description = "Fine-grained GitHub token for this repository with Actions read and write, nothing else. Only needed to create the connection or to replace the token (docs/operations.md); EventBridge keeps it in Secrets Manager."
  type        = string
  default     = ""
  sensitive   = true
}

locals {
  verify_workflow_dispatch = "https://api.github.com/repos/permaily-org/permaily/actions/workflows/verify-production.yml/dispatches"
}

resource "aws_cloudwatch_event_connection" "github" {
  name               = "permaily-github-dispatch"
  description        = "GitHub API, with a token that can only start workflows of permaily-org/permaily"
  authorization_type = "API_KEY"

  auth_parameters {
    api_key {
      key   = "Authorization"
      value = "Bearer ${var.github_dispatch_token}"
    }
  }

  lifecycle {
    # Later applies run without the token. To replace it, set the variable and
    # run `tofu apply -replace=aws_cloudwatch_event_connection.github`.
    ignore_changes = [auth_parameters]
  }
}

resource "aws_cloudwatch_event_api_destination" "verify" {
  name                             = "permaily-verify-production"
  description                      = "Starts the verify-production workflow"
  connection_arn                   = aws_cloudwatch_event_connection.github.arn
  invocation_endpoint              = local.verify_workflow_dispatch
  http_method                      = "POST"
  invocation_rate_limit_per_second = 1
}

resource "aws_cloudwatch_event_rule" "verify" {
  name                = "permaily-verify-production"
  description         = "Every half hour, at the minutes the workflow's own schedule names"
  schedule_expression = "cron(7,37 * * * ? *)"
}

resource "aws_cloudwatch_event_target" "verify" {
  rule     = aws_cloudwatch_event_rule.verify.name
  arn      = aws_cloudwatch_event_api_destination.verify.arn
  role_arn = aws_iam_role.verify_dispatch.arn

  # The workflow runs from the default branch, as its schedule does.
  input = jsonencode({ ref = "develop" })

  http_target {
    header_parameters = {
      Accept                 = "application/vnd.github+json"
      "X-GitHub-Api-Version" = "2022-11-28"
    }
  }

  retry_policy {
    maximum_event_age_in_seconds = 300
    maximum_retry_attempts       = 2
  }
}

resource "aws_iam_role" "verify_dispatch" {
  name = "permaily-verify-dispatch"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sts:AssumeRole"
      Condition = { StringEquals = { "aws:SourceAccount" = local.account_id } }
    }]
  })
}

resource "aws_iam_role_policy" "verify_dispatch" {
  name = "invoke-api-destination"
  role = aws_iam_role.verify_dispatch.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "events:InvokeApiDestination"
      Resource = aws_cloudwatch_event_api_destination.verify.arn
    }]
  })
}
