# AWS's prebuilt enclave kernel, init and nsm.ko (aws-nitro-enclaves-cli), the
# only third-party binaries in the image (eif.nix). aws-blobs.json says what
# each one is, under which license, and where its source is and how sure that
# is; CI checks every built image against it (scripts/image-inventory.ts).
# Changing the tag means updating aws-blobs.json in the same change.
{ fetchFromGitHub }:

let
  src = fetchFromGitHub {
    owner = "aws";
    repo = "aws-nitro-enclaves-cli";
    rev = "v1.5.2";
    hash = "sha256-r3VBa/hMoD7Wuq2Fc4NVbdrJkZYj4uziZk24Y1e/1GA=";
  };
in
"${src}/blobs/aarch64"
