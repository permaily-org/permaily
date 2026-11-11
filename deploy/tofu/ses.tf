# The service's own mail: sign-in messages and notices, sent by the enclave
# through SES from a subdomain of its own (ADR 0017). The address is compiled
# into the image (mail_from in deploy/enclave.json).
#
# What the enclave sends is read by AWS and by nobody on the way. Whoever
# administers this account could make SES keep a copy (a configuration set
# with an archive), so the enclave reads from SES whether a configuration set
# applies to this identity, sends only while none does, and reports every
# state it saw on /status, which permaily-verify checks (docs/threat-model.md).
# Never give this identity, or the address, a configuration set.

locals {
  mail_from   = jsondecode(file("${path.module}/../enclave.json")).mail_from
  mail_domain = split("@", local.mail_from)[1]
  # Bounces come back to a name under the same domain, so SPF passes for it.
  mail_bounce_domain = "bounce.${local.mail_domain}"
}

resource "aws_sesv2_email_identity" "mail" {
  email_identity = local.mail_domain

  dkim_signing_attributes {
    next_signing_key_length = "RSA_2048_BIT"
  }
}

resource "aws_sesv2_email_identity_mail_from_attributes" "mail" {
  email_identity         = aws_sesv2_email_identity.mail.email_identity
  mail_from_domain       = local.mail_bounce_domain
  behavior_on_mx_failure = "REJECT_MESSAGE"

  depends_on = [cloudflare_dns_record.mail_bounce_mx]
}

# An address that bounced for good, or whose owner marked the mail as spam,
# is not written to again.
resource "aws_sesv2_account_suppression_attributes" "mail" {
  suppressed_reasons = ["BOUNCE", "COMPLAINT"]
}

# The parent instance's role, which the enclave uses through the host: send
# from that one address, and read the two identities whose settings decide
# who else gets a copy (the domain, and the address, which need not exist).
resource "aws_iam_role_policy" "account_mail" {
  name = "account-mail"
  role = aws_iam_role.enclave_host.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect    = "Allow"
        Action    = "ses:SendEmail"
        Resource  = aws_sesv2_email_identity.mail.arn
        Condition = { StringEquals = { "ses:FromAddress" = local.mail_from } }
      },
      {
        Effect = "Allow"
        Action = "ses:GetEmailIdentity"
        Resource = [
          aws_sesv2_email_identity.mail.arn,
          "arn:aws:ses:${var.aws_region}:${local.account_id}:identity/${local.mail_from}",
        ]
      },
    ]
  })
}

# --- DNS: DKIM, the bounce domain (SPF) and DMARC. ---

resource "cloudflare_dns_record" "mail_dkim" {
  count   = 3
  zone_id = data.cloudflare_zone.main.zone_id
  name    = "${aws_sesv2_email_identity.mail.dkim_signing_attributes[0].tokens[count.index]}._domainkey.${local.mail_domain}"
  type    = "CNAME"
  content = "${aws_sesv2_email_identity.mail.dkim_signing_attributes[0].tokens[count.index]}.dkim.amazonses.com"
  proxied = false
  ttl     = 300
  comment = "DKIM of the service's own mail, signed by SES (managed by OpenTofu)"
}

resource "cloudflare_dns_record" "mail_bounce_mx" {
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = local.mail_bounce_domain
  type     = "MX"
  content  = "feedback-smtp.${var.aws_region}.amazonses.com"
  priority = 10
  ttl      = 300
  comment  = "Bounces of the service's own mail go back to SES (managed by OpenTofu)"
}

resource "cloudflare_dns_record" "mail_bounce_spf" {
  zone_id = data.cloudflare_zone.main.zone_id
  name    = local.mail_bounce_domain
  type    = "TXT"
  content = "\"v=spf1 include:amazonses.com -all\""
  ttl     = 300
  comment = "Only SES sends the service's own mail (managed by OpenTofu)"
}

# Nothing else sends as this domain, so a message that fails both checks is
# refused. DKIM is signed with the domain itself (strict); SPF passes for the
# bounce name under it (relaxed).
resource "cloudflare_dns_record" "mail_dmarc" {
  zone_id = data.cloudflare_zone.main.zone_id
  name    = "_dmarc.${local.mail_domain}"
  type    = "TXT"
  content = "\"v=DMARC1; p=reject; adkim=s; aspf=r\""
  ttl     = 300
  comment = "DMARC of the service's own mail (managed by OpenTofu)"
}

output "mail_identity" {
  value = aws_sesv2_email_identity.mail.email_identity
}
