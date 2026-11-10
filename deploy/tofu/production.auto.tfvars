# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# Every release that has a KMS key of its own, by PCR0. Only that release's
# image can use its key, and nobody can change that (ADR 0013). Add a release
# before handing it the root secret; remove one to delete its key once no
# rollback to it is planned. Never remove the release that is running.
release_keys = {
  "v0.7.0" = "4845932fd56ce95b357a5dc910d2456904c5bb4de1a386c3984d273dd5ef38c9d0efb4d57d392f224fe325e1768adbc0"
}

# The old mail key: v0.7.0 used it once, to move the data keys of earlier
# releases under the root secret. No image may use it any more.
enclave_pcr0_allowlist = []

# Retired on 2026-11-10: nobody can use the old mail key or change its policy
# again. It cannot be undone.
mail_key_retired = true
