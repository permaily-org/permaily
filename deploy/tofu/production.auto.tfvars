# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.14.0" = "1bf650c9ad46c00be881ec1066f7cdee150fc9ad6ce846cc95409cf009b39e2d496ae2b4053b5ec0cf859232f4b961e2"
  "v0.15.0" = "bde3304bc04ab6aa46fa80b0b0748907efea1f9083455c211f769f27cd9db11e2134861d710097dfce20412dfa7bf0d7"
  "v0.16.0" = "faacaecf08d792555c553ae74350f9bd5604fc323fe177548b8c0a6fb1af3ece6b513729741e64e8172e825d0c199aa5"
  "v0.16.1" = "b8937690736d10c0fb10d737182b9c8c0ce171d9e164dbf39dc3d434ee2690bdf7b6c6743023ac920ac4201a8186b72c"
}

# Where senders send SMTP TLS reports: the "TLS reports" mailbox the super admin
# made after v0.12.0 (docs/operations.md). Public on purpose.
tls_rpt_address = "skr25i9u9s364ra7a6a9@inbox.permaily.com"

# Operators other than the owner, each the IAM user permaily-operator-<label>,
# who can deploy and operate after signing in with MFA (deploy/tofu/operators.tf,
# docs/operations.md, "Operators"). Labels, never people's names: this file is
# public. No credential is in the state.
operators = ["2"]

# Operators who may add their first MFA device without MFA, and make no access
# key meanwhile. Emptied once they have signed in with MFA.
operators_enrolling = ["2"]

# The drift check's image in ECR (docs/operations.md, "The drift check"). Public on purpose.
drift_image_digest = "sha256:e827d6524fe50c94d08174a479d02b2de8fdef571b4debcb39f4a9a06a016106"
