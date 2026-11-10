# Public site (site/, Astro) on AWS Amplify: main at permaily.com and www,
# develop at beta.permaily.com, plus pull request previews.
# Not verifiable and not part of the enclave: the web app inside the enclave is
# app.permaily.com (main.tf).
#
# Repository access uses the Amplify GitHub App (installed on permaily-org for
# the permaily repo). Creating the app needs a GitHub token with admin:repo_hook
# once; Amplify does not store it, so it can be revoked after the first apply.

variable "github_access_token" {
  description = "GitHub token with admin:repo_hook, only needed to create the Amplify app."
  type        = string
  default     = null
  sensitive   = true
}


resource "aws_amplify_app" "site" {
  name         = "permaily-site"
  repository   = "https://github.com/permaily-org/permaily"
  access_token = var.github_access_token
  platform     = "WEB"

  # The build is defined in amplify.yml at the repo root (monorepo, app root site/).
  environment_variables = {
    AMPLIFY_MONOREPO_APP_ROOT = "site"
  }

  # Security headers. Script and style hashes are in each page's CSP <meta>
  # (site/astro.config.mjs); these are the directives that only work as headers.
  # Amplify ignores customHeaders in amplify.yml for monorepo apps, so they live here.
  custom_headers = jsonencode({
    applications = [{
      appRoot = "site"
      customHeaders = [
        {
          pattern = "**"
          headers = [
            { key = "Strict-Transport-Security", value = "max-age=31536000; includeSubDomains" },
            { key = "X-Content-Type-Options", value = "nosniff" },
            { key = "X-Frame-Options", value = "DENY" },
            { key = "Referrer-Policy", value = "strict-origin-when-cross-origin" },
            { key = "Permissions-Policy", value = "camera=(), microphone=(), geolocation=(), payment=(), usb=()" },
            { key = "Content-Security-Policy", value = "frame-ancestors 'none'; base-uri 'none'; object-src 'none'" },
          ]
        },
        {
          pattern = "_astro/**"
          headers = [
            { key = "Cache-Control", value = "public, max-age=31536000, immutable" },
          ]
        },
      ]
    }]
  })

  # Serve the site's own 404 page for unknown paths.
  custom_rule {
    source = "/<*>"
    target = "/404.html"
    status = "404"
  }

  lifecycle {
    # The token is only used at creation; later applies run without it.
    ignore_changes = [access_token]
  }
}

resource "aws_amplify_branch" "main" {
  app_id            = aws_amplify_app.site.id
  branch_name       = "main"
  stage             = "PRODUCTION"
  framework         = "Astro"
  enable_auto_build = true
}

# develop is served at beta.permaily.com. Pull requests into develop get their
# own preview URL (pr-<n>.<app>.amplifyapp.com). Previews skip the build when the
# pull request does not touch site/ (AMPLIFY_DIFF_DEPLOY); a change only in
# packages/ui/ or amplify.yml is still checked by the site job in CI, without a
# hosted preview. The app has no IAM service role, so preview builds of fork
# pull requests get no AWS access.
resource "aws_amplify_branch" "develop" {
  app_id                      = aws_amplify_app.site.id
  branch_name                 = "develop"
  stage                       = "BETA"
  framework                   = "Astro"
  enable_auto_build           = true
  enable_pull_request_preview = true
  environment_variables = {
    AMPLIFY_DIFF_DEPLOY      = "true"
    AMPLIFY_DIFF_DEPLOY_ROOT = "site"
  }
}

# The site's own certificate, for exactly its three names. Amplify's managed
# certificate also covers *.permaily.com, which is valid for app., api., mcp.
# and inbox.: a key outside the enclave that browsers would accept for them.
locals {
  site_names = [var.domain, "www.${var.domain}", "beta.${var.domain}"]
}

resource "aws_acm_certificate" "site" {
  provider                  = aws.us_east_1
  domain_name               = local.site_names[0]
  subject_alternative_names = slice(local.site_names, 1, length(local.site_names))
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "cloudflare_dns_record" "site_acm_validation" {
  for_each = toset(local.site_names)
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = trimsuffix(one([for o in aws_acm_certificate.site.domain_validation_options : o.resource_record_name if o.domain_name == each.key]), ".")
  type     = "CNAME"
  content  = trimsuffix(one([for o in aws_acm_certificate.site.domain_validation_options : o.resource_record_value if o.domain_name == each.key]), ".")
  proxied  = false
  ttl      = 300
  comment  = "ACM validation of the public site's certificate (managed by OpenTofu)"
}

resource "aws_acm_certificate_validation" "site" {
  provider                = aws.us_east_1
  certificate_arn         = aws_acm_certificate.site.arn
  validation_record_fqdns = [for r in cloudflare_dns_record.site_acm_validation : r.name]
}

resource "aws_amplify_domain_association" "site" {
  app_id                = aws_amplify_app.site.id
  domain_name           = var.domain
  wait_for_verification = false

  certificate_settings {
    type                   = "CUSTOM"
    custom_certificate_arn = aws_acm_certificate_validation.site.certificate_arn
  }

  sub_domain {
    branch_name = aws_amplify_branch.main.branch_name
    prefix      = ""
  }

  sub_domain {
    branch_name = aws_amplify_branch.main.branch_name
    prefix      = "www"
  }

  sub_domain {
    branch_name = aws_amplify_branch.develop.branch_name
    prefix      = "beta"
  }
}

locals {
  # " CNAME d123.cloudfront.net" for the apex, "www CNAME d123.cloudfront.net" for www.
  site_targets = {
    for s in aws_amplify_domain_association.site.sub_domain :
    s.prefix => trimsuffix(reverse(split(" ", trimspace(s.dns_record)))[0], ".")
  }
}

# Cloudflare flattens the apex CNAME, so it can live next to the MX records.
resource "cloudflare_dns_record" "site" {
  for_each = toset(["", "www", "beta"]) # must match the sub_domain prefixes above
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = each.key == "" ? var.domain : "${each.key}.${var.domain}"
  type     = "CNAME"
  content  = local.site_targets[each.key]
  proxied  = false
  ttl      = 300
  comment  = "Public site on Amplify (managed by OpenTofu)"
}

# The site's certificate comes from Amazon (ACM). The enclave subdomains keep
# their own CAA records (letsencrypt.org only), which take precedence over this one.
resource "cloudflare_dns_record" "apex_caa" {
  for_each = toset(["amazon.com", "amazontrust.com", "awstrust.com", "amazonaws.com"])
  zone_id  = data.cloudflare_zone.main.zone_id
  name     = var.domain
  type     = "CAA"
  ttl      = 300
  data = {
    flags = 0
    tag   = "issue"
    value = each.value
  }
  comment = "ACM certificate for the public site (managed by OpenTofu)"
}

output "site_app_id" {
  value = aws_amplify_app.site.id
}

output "site_default_domain" {
  value = aws_amplify_app.site.default_domain
}

# Nobody may issue a wildcard for the domain: *.permaily.com would be valid for
# the enclave hosts, and their own CAA records do not stop a wildcard issued at
# the apex. Cloudflare's Universal SSL is disabled for the zone for the same
# reason (it issued wildcards and added its own issuewild records here).
resource "cloudflare_dns_record" "apex_caa_wild" {
  zone_id = data.cloudflare_zone.main.zone_id
  name    = var.domain
  type    = "CAA"
  ttl     = 300
  data = {
    flags = 0
    tag   = "issuewild"
    value = ";"
  }
  comment = "No wildcard certificates for permaily.com (managed by OpenTofu)"
}

# The footer shows the latest release's PCR0, read at build time. A release is
# published after main was already built, so release.yml calls this webhook to
# rebuild main once the new pcrs.json exists. Its URL is a secret
# (AMPLIFY_RELEASE_WEBHOOK in GitHub).
resource "aws_amplify_webhook" "release" {
  app_id      = aws_amplify_app.site.id
  branch_name = aws_amplify_branch.main.branch_name
  description = "Rebuild main after a GitHub release"
}

output "site_release_webhook_url" {
  value     = aws_amplify_webhook.release.url
  sensitive = true
}
