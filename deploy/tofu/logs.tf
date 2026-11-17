# The journal of the parent instance's units (permaily-host, which prints the
# enclave's log lines, and permaily-enclave) kept in CloudWatch Logs for 90
# days, so that it outlives the instance (ADR 0034). The CloudWatch agent on
# the instance reads the journal and writes here (deploy/host/install-logs.sh,
# run by scripts/deploy.sh). The lines are kinds only, never content
# (docs/threat-model.md, "Logging"); whoever reads this group is whoever has
# CloudWatch Logs read permissions in the account.

resource "aws_cloudwatch_log_group" "host" {
  name              = "permaily-host"
  retention_in_days = 90
}

# The instance may add streams and lines to that group, and nothing else: it
# cannot read, delete or change the retention.
resource "aws_iam_role_policy" "host_logs" {
  name = "host-logs"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
      Resource = "${aws_cloudwatch_log_group.host.arn}:*"
    }]
  })
}

output "host_log_group" {
  description = "CloudWatch Logs group with the journal of permaily-host and permaily-enclave (90 days)."
  value       = aws_cloudwatch_log_group.host.name
}
