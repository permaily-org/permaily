# The hourly port 25 check (ADR 0038): a t4g.nano of its own runs
# `permaily-verify -require-smtp` against production every hour, at minute 41,
# with the latest release's verifier and pcrs.json checked against their
# provenance first (deploy/port25/verify-cron.sh, a copy of scripts/'s), and
# mails the subscribers of the alarms' topic (alarms.tf) when it fails or
# cannot run.
#
# It is not the production instance and reaches nothing of it: its own
# security group (nothing in; out to 443 and 25 only), its own role (publish to
# the alarms' topic, read its GitHub token, SSM), no key, no bucket, no table.
#
# AWS blocks outbound port 25 from EC2 until it lifts the account's block on
# request, and the check fails every hour without it: the instance exists only
# once `port25_check` is true (docs/operations.md, "Port 25, from its
# instance"). The security group, the role and the secret exist before, so
# that the token can be put first.

variable "port25_check" {
  description = "Runs the hourly port 25 check on its own instance (ADR 0038). Leave false until AWS has lifted the account's block of outbound port 25: before that the check fails and mails every hour."
  type        = bool
  default     = false
}

locals {
  port25 = var.port25_check ? 1 : 0

  # gh, from GitHub's release, pinned by version and SHA-256 (its checksums file,
  # and `gh attestation verify --repo cli/cli` of the archive). To update it,
  # check the new archive the same way and change both together.
  port25_gh_version = "2.103.3"
  port25_gh_sha256  = "ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63"
  # The first boot's download of gh, retries included, ends within this many
  # seconds, or the boot fails and mails (cloud-init.sh.tftpl).
  port25_download_timeout = 600
  # And its mail that it failed, within this many.
  port25_setup_alert_timeout = 60

  # The scripts the instance runs, as they are in this configuration. Their
  # full-line comments are taken out so that all of it fits in user data
  # (16 KiB); none of these scripts has a line starting with # inside a string.
  # Everything read here is under deploy/: the drift check fetches only that
  # (deploy/drift/drift.sh), so verify-cron.sh and verify-production.sh are
  # copies of scripts/'s, which scripts/port25.test.ts keeps identical.
  port25_scripts = {
    for name, file_path in {
      verify_cron       = "${path.module}/../port25/verify-cron.sh"
      verify_production = "${path.module}/../port25/verify-production.sh"
      run               = "${path.module}/../port25/run.sh"
      alert             = "${path.module}/../port25/alert.sh"
      on_failure        = "${path.module}/../port25/on-failure.sh"
    } :
    name => join("\n", [for i, line in split("\n", chomp(file(file_path))) : line if i == 0 || !startswith(line, "#")])
  }

  port25_user_data = templatefile("${path.module}/../port25/cloud-init.sh.tftpl", merge(local.port25_scripts, {
    region           = var.aws_region
    topic_arn        = aws_sns_topic.sigstore_alarm.arn
    token_secret     = aws_secretsmanager_secret.port25_github.arn
    gh_version       = local.port25_gh_version
    gh_sha256        = local.port25_gh_sha256
    download_timeout = local.port25_download_timeout
    alert_timeout    = local.port25_setup_alert_timeout
    service_unit     = chomp(file("${path.module}/../port25/permaily-port25.service"))
    failed_unit      = chomp(file("${path.module}/../port25/permaily-port25-failed.service"))
    timer_unit       = chomp(file("${path.module}/../port25/permaily-port25.timer"))
  }))
}

# The token gh signs in with: fine-grained, public repositories read-only, put
# by the operator (docs/operations.md), so it is in neither the repository nor
# the state. GitHub's CLI refuses to download releases or check attestations
# without one.
resource "aws_secretsmanager_secret" "port25_github" {
  name        = "permaily/port25/github-token"
  description = "Fine-grained GitHub token, public repositories read-only, for the hourly port 25 check (gh release download, gh attestation verify)"
}

# Nothing comes in; access is through SSM. Out: 443 (GitHub, Sigstore, AWS's
# endpoints, the enclave's HTTPS, DNS over HTTPS for CAA) and 25 (the MX).
# Amazon's DNS resolver, the instance metadata and the time service are not
# filtered by security groups, so 53 is not open. Inline rules, as for the
# enclave host: a separate rule resource is removed on the next apply.
resource "aws_security_group" "port25" {
  name        = "permaily-port25-check"
  description = "The hourly port 25 check: nothing in; out to 443 and 25 only"
  vpc_id      = aws_vpc.main.id

  egress {
    description = "HTTPS: GitHub, Sigstore, AWS endpoints, the enclave, DNS over HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "SMTP to the enclave MX (permaily-verify -require-smtp)"
    from_port   = 25
    to_port     = 25
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "permaily-port25-check" }
}

resource "aws_iam_role" "port25" {
  name = "permaily-port25-check"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# SSM for administration, as on the enclave host.
resource "aws_iam_role_policy_attachment" "port25_ssm" {
  role       = aws_iam_role.port25.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Besides SSM: the alarm and its token, and nothing else. The managed policy
# above lets an instance read any SSM parameter; this one may read none of the
# account's (AWS's public ones, such as the Amazon Linux image, stay readable).
resource "aws_iam_role_policy" "port25" {
  name = "alarm-and-token"
  role = aws_iam_role.port25.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "Alarm"
        Effect   = "Allow"
        Action   = "sns:Publish"
        Resource = aws_sns_topic.sigstore_alarm.arn
      },
      {
        Sid      = "GitHubToken"
        Effect   = "Allow"
        Action   = "secretsmanager:GetSecretValue"
        Resource = aws_secretsmanager_secret.port25_github.arn
      },
      {
        Sid      = "NoParametersOfTheAccount"
        Effect   = "Deny"
        Action   = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssm:GetParameterHistory"]
        Resource = "arn:aws:ssm:*:${local.account_id}:parameter/*"
      },
    ]
  })
}

resource "aws_iam_instance_profile" "port25" {
  name = "permaily-port25-check"
  role = aws_iam_role.port25.name
}

resource "aws_instance" "port25" {
  count = local.port25

  ami                         = data.aws_ssm_parameter.al2023_arm64.insecure_value
  instance_type               = "t4g.nano"
  subnet_id                   = aws_subnet.public.id
  associate_public_ip_address = true
  vpc_security_group_ids      = [aws_security_group.port25.id]
  iam_instance_profile        = aws_iam_instance_profile.port25.name

  # No surplus credits billed: a run is a few seconds of CPU an hour.
  credit_specification {
    cpu_credits = "standard"
  }

  metadata_options {
    http_tokens                 = "required"
    http_endpoint               = "enabled"
    http_put_response_hop_limit = 1
  }

  root_block_device {
    volume_type = "gp3"
    volume_size = 8
    encrypted   = true
  }

  user_data                   = local.port25_user_data
  user_data_replace_on_change = true

  lifecycle {
    # A newer Amazon Linux image is taken when the instance is replaced, not
    # on every plan (the drift check would mail each new image).
    ignore_changes = [ami]

    # length() counts characters: with ASCII only, they are the bytes EC2
    # counts.
    precondition {
      condition     = length(local.port25_user_data) <= 16384 && can(regex("^[[:ascii:]]*$", local.port25_user_data))
      error_message = "The port 25 check's user data is over 16 KiB or not ASCII: shorten deploy/port25 or the scripts it carries, and keep them ASCII."
    }
  }

  depends_on = [aws_iam_role_policy.port25, aws_iam_role_policy_attachment.port25_ssm]

  tags = { Name = "permaily-port25-check" }
}

# The check mails what it finds and when its own run fails (deploy/port25).
# This mails when the instance cannot run it: its status checks fail, or it
# reports nothing for 15 minutes (stopped or terminated); and again when it
# clears.
resource "aws_cloudwatch_metric_alarm" "port25_down" {
  count = local.port25

  alarm_name          = "permaily-port25-down"
  alarm_description   = "The port 25 check's instance failed its status checks or stopped reporting: no hourly port 25 check runs (docs/operations.md, \"Port 25, from its instance\")."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  dimensions          = { InstanceId = aws_instance.port25[0].id }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [aws_sns_topic.sigstore_alarm.arn]
  ok_actions          = [aws_sns_topic.sigstore_alarm.arn]
}

output "port25_instance" {
  description = "Instance of the hourly port 25 check, for SSM; empty while port25_check is false."
  value       = try(aws_instance.port25[0].id, "")
}
