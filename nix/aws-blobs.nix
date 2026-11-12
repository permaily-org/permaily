# AWS's prebuilt enclave kernel, init and nsm.ko (aws-nitro-enclaves-cli),
# shared by the image (eif.nix) and the archive of their source (gpl-source.nix)
# so the two cannot be built from different blobs.
{ fetchFromGitHub }:

let
  src = fetchFromGitHub {
    owner = "aws";
    repo = "aws-nitro-enclaves-cli";
    rev = "v1.5.2";
    hash = "sha256-r3VBa/hMoD7Wuq2Fc4NVbdrJkZYj4uziZk24Y1e/1GA=";
  };
in
{
  inherit src;
  dir = "${src}/blobs/aarch64";
}
