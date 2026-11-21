# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.16.1" = "b8937690736d10c0fb10d737182b9c8c0ce171d9e164dbf39dc3d434ee2690bdf7b6c6743023ac920ac4201a8186b72c"
  "v0.17.0" = "b42fa439f5029c06e0ce32a29d34d4be0d3da0d576f1905e35db6eb583ebec8746abe7de38011c89be3f303d8bfd1dad"
  "v0.18.0" = "e06bbe380bd233d4796ce7ad928ef3d69704eee621deed3805dbe87e9cabf2aceef4fafbd808ac18a2723f57e635cda8"
  "v0.18.1" = "a13bf57a16954abc06cbd7cf4d8f7af6cbcb29a90578d4d65d2024cfe246d40b99707951117bbf8fa573c4940f103195"
  "v0.18.2" = "864eec43908b2718daf7f0845db091c74118d766ffb9841b63cfee2ff85cbdf62a8d06eb2d8de40704e941eddae4e9c5"
}

# Where senders send SMTP TLS reports: the "TLS reports" mailbox the super admin
# made after v0.12.0 (docs/operations.md). Public on purpose.
tls_rpt_address = "skr25i9u9s364ra7a6a9@inbox.permaily.com"

# The drift check's image in ECR (docs/operations.md, "The drift check"). Public on purpose.
drift_image_digest = "sha256:e827d6524fe50c94d08174a479d02b2de8fdef571b4debcb39f4a9a06a016106"
