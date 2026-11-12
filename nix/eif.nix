# Assembles the enclave image (EIF) and its PCRs.
#
# Everything that goes into the image is fixed:
#   - kernel, kernel config, cmdline, init and nsm.ko: AWS's prebuilt blobs
#     from aws-nitro-enclaves-cli (trusting AWS is in scope). Using the prebuilt
#     init (instead of compiling it) keeps PCRs independent of the build host.
#     Pinned in aws-blobs.nix and described in aws-blobs.json.
#   - rootfs: only the static permaily-enclave binary, copied flat. No
#     /nix/store paths end up in the image.
#   - entrypoint and environment: below, one argument per line (AWS init splits
#     /cmd and /env on newlines).
{ lib, runCommand, jq, nitro, awsBlobs, enclaveBin, acmeDirectory
, staging ? false
, gomemlimit ? "1536MiB"
}:

let
  # Runtime configuration compiled into the image (and therefore into PCR0).
  # Edit deploy/enclave.json, never pass configuration from the parent instance.
  # A staging image is a deployment of its own: the "staging" object replaces
  # the values that keep it apart from production (its storage, and which
  # builds may receive its root secret).
  base = builtins.fromJSON (builtins.readFile ../deploy/enclave.json);
  config = base // lib.optionalAttrs staging base.staging;
  # An empty value is left out: AWS's init splits the entry point on newlines.
  flag = name: value: lib.optionals (value != "") [ name value ];

  blobs = awsBlobs;

  rootfs = runCommand "permaily-enclave-rootfs" { } ''
    mkdir -p $out
    cp ${enclaveBin}/bin/permaily-enclave $out/permaily-enclave
  '';

  image = nitro.buildEif {
    name = "permaily-enclave";
    version = "0";
    arch = "aarch64";
    kernel = "${blobs}/Image";
    kernelConfig = "${blobs}/Image.config";
    cmdline = lib.removeSuffix "\n" (builtins.readFile "${blobs}/cmdline");
    init = "${blobs}/init";
    nsmKo = "${blobs}/nsm.ko";
    copyToRoot = rootfs;
    copyToRootWithClosure = false;
    entrypoint = lib.concatStringsSep "\n" ([
      "/permaily-enclave"
      "-domain" config.domain
      "-acme-directory" acmeDirectory
      "-mailbox-id" config.mailbox_id
      "-mailbox-address" config.mailbox_address
      "-mailbox-token-sha256" config.mailbox_token_sha256
      "-mta-sts-mode" config.mta_sts_mode
      "-aws-region" config.aws_region
      "-aws-account" config.aws_account_id
      "-dynamodb-table" config.dynamodb_table
      "-s3-bucket" config.s3_bucket
      "-admin-token-sha256" config.admin_token_sha256
      "-release-repository" config.release_repository
      "-release-repository-id" config.release_repository_id
      "-release-owner-id" config.release_owner_id
      "-release-workflow" config.release_workflow
    ]
    ++ flag "-release-ref" config.release_ref
    ++ flag "-release-ref-prefix" config.release_ref_prefix
    ++ flag "-anchor-bucket" config.anchor_bucket
    ++ flag "-anchor-retention-days" (toString config.anchor_retention_days)
    ++ flag "-mail-from" (config.mail_from or "")
    ++ flag "-super-admin" (config.super_admin or ""));
    env = "GOMEMLIMIT=${gomemlimit}";
  };
in
runCommand "permaily-eif" { nativeBuildInputs = [ jq ]; } ''
  mkdir -p $out
  cp ${image}/image.eif $out/permaily.eif
  jq '{PCR0, PCR1, PCR2}' ${image}/pcr.json > $out/pcrs.json
  (cd $out && sha256sum permaily.eif pcrs.json > SHA256SUMS)
  cat $out/pcrs.json
''
