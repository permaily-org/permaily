# The archive of the source of the third-party programs in the enclave image
# (permaily-gpl-source.tar.gz), published next to permaily.eif with every
# release: GPL-2.0 section 3(a) for AWS's kernel and nsm.ko, and LGPL-2.1
# section 6 for the GNU C Library inside AWS's init.
#
# gpl-source/manifest.json says, for each binary in the image, its SHA-256,
# which files here are its source, and how certain that is (gpl-source/about.md,
# the archive's README.md, explains each). The build fails when a blob of
# aws-blobs.nix is not the binary the manifest describes, so updating the blobs
# without establishing their source breaks the build. scripts/gpl-source.ts
# checks a built archive against an image.
#
# Nothing here goes into the image: its PCRs do not depend on this file.
{ runCommand, fetchFromGitHub, jq, awsBlobs }:

let
  # aws-nitro-enclaves-sdk-bootstrap at the commit that updated the kernel
  # configuration for the kernel and nsm.ko of the blobs (2022-02-02).
  bootstrapKernel = fetchFromGitHub {
    owner = "aws";
    repo = "aws-nitro-enclaves-sdk-bootstrap";
    rev = "ed24913346a34d719afa2031299253160a2e3460";
    hash = "sha256-Pnx6vX85OS/I/JdAWjsygH54oY4RGubewf3CeubT6gw=";
  };
  # ... and at v1.0.0, the init.c of the init blob (2024-05-22).
  bootstrapInit = fetchFromGitHub {
    owner = "aws";
    repo = "aws-nitro-enclaves-sdk-bootstrap";
    rev = "203242d54e4fbb9b5d57b49e73c947226e08ce82";
    hash = "sha256-EFpH0UjBKhKzPLPQmbYPcRml9Zw5pJmT6m6Jy1mzfC8=";
  };
in
runCommand "permaily-gpl-source" { nativeBuildInputs = [ jq ]; } ''
  set -eu
  d=permaily-gpl-source
  mkdir -p $d/aws-nitro-enclaves-cli/blobs/aarch64
  cp ${./gpl-source/about.md} $d/README.md
  cp ${./gpl-source/manifest.json} $d/manifest.json
  cp ${./gpl-source/gpl-2.0.txt} $d/COPYING
  cp ${./gpl-source/lgpl-2.1.txt} $d/COPYING.LGPL-2.1
  cp ${awsBlobs.dir}/Image.config ${awsBlobs.dir}/cmdline $d/aws-nitro-enclaves-cli/blobs/aarch64/
  cp -r ${bootstrapKernel} $d/aws-nitro-enclaves-sdk-bootstrap-ed24913
  cp -r ${bootstrapInit} $d/aws-nitro-enclaves-sdk-bootstrap-203242d
  chmod -R u+w $d

  # The blobs the image is built from are the binaries the manifest describes,
  # and every file it names as their source is here.
  jq -r '.components[] | "\(.blob) \(.sha256)"' $d/manifest.json | while read -r blob sum; do
    echo "$sum  ${awsBlobs.dir}/$blob" | sha256sum -c -
  done
  jq -r '.components[].files[]' $d/manifest.json | sort -u | while read -r f; do
    [ -f "$d/$f" ] || { echo "manifest.json names $f, which is not in the archive"; exit 1; }
  done

  (cd $d && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS)
  mkdir -p $out
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --mode=u+w,go-w,a+rX \
    --format=gnu -cf - $d | gzip -9n > $out/permaily-gpl-source.tar.gz
  (cd $out && sha256sum permaily-gpl-source.tar.gz > SHA256SUMS)
  cat $out/SHA256SUMS
''
