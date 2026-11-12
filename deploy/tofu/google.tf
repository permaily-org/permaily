# Google Search Console reads this record to know that the Google account that
# owns the sign-in application (Google Auth Platform, project permaily) owns
# the domain; Google shows the name "Permaily" on its sign-in page only after
# that. The value is public and not a secret.
resource "cloudflare_dns_record" "google_site_verification" {
  zone_id = data.cloudflare_zone.main.zone_id
  name    = var.domain
  type    = "TXT"
  content = "\"google-site-verification=4Zn5Le-oysj0_9L4-HYX8P53eXh9MDMuRvx5LXCE3Ak\""
  ttl     = 300
  comment = "Proves to Google that we own the domain, for the sign-in brand (managed by OpenTofu)"
}
