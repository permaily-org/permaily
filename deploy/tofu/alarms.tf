# Mail to the maintainers when the workflow sigstore-watch fails: a deadline
# that must not be missed (docs/operations.md, "Sigstore changed"). The
# workflow publishes to an SNS topic; who receives it are the topic's
# subscriptions, made by hand so that no address is in this repository:
#
#   aws sns subscribe --topic-arn <sigstore_alarm_topic> --protocol email --notification-endpoint <address>
#
# Each address confirms once, from the message AWS sends it.

resource "aws_sns_topic" "sigstore_alarm" {
  name         = "permaily-sigstore-deadline"
  display_name = "Permaily"
}

# What the workflow can do in the account: publish to that topic, and nothing
# else. It signs in as this user, with an access key kept as a secret of the
# repository (SIGSTORE_ALARM_AWS_ACCESS_KEY_ID and
# SIGSTORE_ALARM_AWS_SECRET_ACCESS_KEY). GitHub's keyless sign-in would need
# an identity provider in the account, which the organization the account
# belongs to does not allow. The key is made by hand (docs/operations.md), so
# it is never in the state of OpenTofu.
resource "aws_iam_user" "sigstore_alarm" {
  name = "permaily-sigstore-alarm"
}

resource "aws_iam_user_policy" "sigstore_alarm" {
  name = "publish"
  user = aws_iam_user.sigstore_alarm.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "sns:Publish"
      Resource = aws_sns_topic.sigstore_alarm.arn
    }]
  })
}

output "sigstore_alarm_topic" {
  description = "SNS topic the sigstore-watch workflow publishes to: repository variable SIGSTORE_ALARM_TOPIC_ARN."
  value       = aws_sns_topic.sigstore_alarm.arn
}
