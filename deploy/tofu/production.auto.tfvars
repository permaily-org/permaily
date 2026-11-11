# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.8.0" = "194ff02da909ccc1df0147b197255ee76e1b36aca40a0796dcce228de76a880d5feeb34a129cdc15c40b0ed06818cf28"
  "v0.9.0" = "a2fa342ffde5a62519c8bfaff9f0d5ada401942754d51e7e757d3859fafb4ce586ab9c9c8e708c4a20251d305c7fadc6"
}
