# What production runs. Public on purpose: permaily-verify reads the same values
# from the live service. Change it in the pull request that follows a release
# (docs/operations.md).

# PCR0 of the release that may get keys from KMS: v0.6.0.
enclave_pcr0_allowlist = ["140909f790e9b6708e88949b971067ef7bb4e7a102b6e7a9ce429afde2018ccd470d83834f69c821565453259ecbeee9"]
