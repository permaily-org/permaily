# Starts two workflows on time, because GitHub's own schedule starts them a few
# times a day at most (docs/operations.md): verify-production every half hour,
# and sigstore-watch once a day. EventBridge calls GitHub's API; each workflow
# keeps its schedule as a second trigger that the operator of this account does
# not time.
#
# This only says "run now": the checks run on GitHub, with the code of the
# repository, and their results are public there. Nothing here is inside the
# enclave or on the instance.

variable "github_dispatch_token" {
  description = "Fine-grained GitHub token for this repository with Actions read and write, nothing else. Only needed to create the connection or to replace the token (docs/operations.md); EventBridge keeps it in Secrets Manager."
  type        = string
  default     = ""
  sensitive   = true
}

locals {
  # Workflow file => when EventBridge starts it (UTC).
  dispatched_workflows = {
    "verify-production" = "cron(7,37 * * * ? *)"
    "sigstore-watch"    = "cron(53 6 * * ? *)"
  }
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

resource "aws_cloudwatch_event_api_destination" "dispatch" {
  for_each = local.dispatched_workflows

  name                             = "permaily-${each.key}"
  description                      = "Starts the ${each.key} workflow"
  connection_arn                   = aws_cloudwatch_event_connection.github.arn
  invocation_endpoint              = "https://api.github.com/repos/permaily-org/permaily/actions/workflows/${each.key}.yml/dispatches"
  http_method                      = "POST"
  invocation_rate_limit_per_second = 1
}

resource "aws_cloudwatch_event_rule" "dispatch" {
  for_each = local.dispatched_workflows

  name                = "permaily-${each.key}"
  description         = "Starts the ${each.key} workflow, at the minutes its own schedule names"
  schedule_expression = each.value
}

resource "aws_cloudwatch_event_target" "dispatch" {
  for_each = local.dispatched_workflows

  rule     = aws_cloudwatch_event_rule.dispatch[each.key].name
  arn      = aws_cloudwatch_event_api_destination.dispatch[each.key].arn
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

moved {
  from = aws_cloudwatch_event_api_destination.verify
  to   = aws_cloudwatch_event_api_destination.dispatch["verify-production"]
}

moved {
  from = aws_cloudwatch_event_rule.verify
  to   = aws_cloudwatch_event_rule.dispatch["verify-production"]
}

moved {
  from = aws_cloudwatch_event_target.verify
  to   = aws_cloudwatch_event_target.dispatch["verify-production"]
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
      Resource = [for d in aws_cloudwatch_event_api_destination.dispatch : d.arn]
    }]
  })
}
