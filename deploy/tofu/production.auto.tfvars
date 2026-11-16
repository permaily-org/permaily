# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.10.0" = "c2999bd871d98ddd75e2c505b5e15cf6d978c227aa9fde535568641026149d5982b4200af66191d5759dffc25d2a5bbb"
  "v0.11.0" = "d8572f3a6bfb12162e89aac19d2609cb53992da8d898b28c7a5dbd5519f8d55a29f9eb03abe3e92cabbe619e777f304d"
  "v0.14.0" = "1bf650c9ad46c00be881ec1066f7cdee150fc9ad6ce846cc95409cf009b39e2d496ae2b4053b5ec0cf859232f4b961e2"
  "v0.15.0" = "bde3304bc04ab6aa46fa80b0b0748907efea1f9083455c211f769f27cd9db11e2134861d710097dfce20412dfa7bf0d7"
}

# Where senders send SMTP TLS reports: the "TLS reports" mailbox the super admin
# made after v0.12.0 (docs/operations.md). Public on purpose.
tls_rpt_address = "skr25i9u9s364ra7a6a9@inbox.permaily.com"
