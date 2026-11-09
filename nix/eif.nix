# Assembles the enclave image (EIF) and its PCRs.
#
# Everything that goes into the image is fixed:
#   - kernel, kernel config, cmdline, init and nsm.ko: AWS's prebuilt blobs
#     from aws-nitro-enclaves-cli (trusting AWS is in scope). Using the prebuilt
#     init (instead of compiling it) keeps PCRs independent of the build host.
#   - rootfs: only the static permaily-enclave binary, copied flat. No
#     /nix/store paths end up in the image.
#   - entrypoint and environment: below, one argument per line (AWS init splits
#     /cmd and /env on newlines).
{ lib, runCommand, fetchFromGitHub, jq, nitro, enclaveBin, acmeDirectory
, gomemlimit ? "1536MiB"
}:

let
  # Runtime configuration compiled into the image (and therefore into PCR0).
  # Edit deploy/enclave.json, never pass configuration from the parent instance.
  config = builtins.fromJSON (builtins.readFile ../deploy/enclave.json);

  awsNitroCli = fetchFromGitHub {
    owner = "aws";
    repo = "aws-nitro-enclaves-cli";
    rev = "v1.5.2";
    hash = "sha256-r3VBa/hMoD7Wuq2Fc4NVbdrJkZYj4uziZk24Y1e/1GA=";
  };
  blobs = "${awsNitroCli}/blobs/aarch64";

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
    entrypoint = lib.concatStringsSep "\n" [
      "/permaily-enclave"
      "-domain" config.domain
      "-acme-directory" acmeDirectory
      "-mailbox-id" config.mailbox_id
      "-mailbox-address" config.mailbox_address
      "-mailbox-token-sha256" config.mailbox_token_sha256
      "-mta-sts-mode" config.mta_sts_mode
      "-aws-region" config.aws_region
      "-kms-key-arn" config.kms_key_arn
      "-dynamodb-table" config.dynamodb_table
      "-s3-bucket" config.s3_bucket
    ];
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
