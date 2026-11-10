# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.7.0" = "4845932fd56ce95b357a5dc910d2456904c5bb4de1a386c3984d273dd5ef38c9d0efb4d57d392f224fe325e1768adbc0"
  "v0.8.0" = "194ff02da909ccc1df0147b197255ee76e1b36aca40a0796dcce228de76a880d5feeb34a129cdc15c40b0ed06818cf28"
}
