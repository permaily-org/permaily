# Builds one Go command as a static binary for linux/<goarch>.
#
# Two rules keep the binary byte-identical on x86_64 and aarch64 builders:
#   - The compiler is the official Go release from go.dev, not nixpkgs' Go.
#     nixpkgs patches the standard library to embed /nix/store paths (tzdata,
#     iana-etc, mailcap), and those paths differ between build platforms.
#   - Cross-compilation is done by the Go toolchain itself (GOARCH), which
#     produces the same output regardless of the host architecture.
# Go modules are vendored by a fixed-output derivation (vendorHash).
{ lib, stdenvNoCC, buildGoModule, fetchurl, rev, subPackage, goarch, web ? null }:

let
  goVersion = "1.26.11";
  goToolchain = {
    x86_64-linux = fetchurl {
      url = "https://go.dev/dl/go${goVersion}.linux-amd64.tar.gz";
      hash = "sha256-mEn04E7jUwNxTjT7w6yKtE6j5EipWqGC+R47m3nHEOw=";
    };
    aarch64-linux = fetchurl {
      url = "https://go.dev/dl/go${goVersion}.linux-arm64.tar.gz";
      hash = "sha256-bC8ptFi1rdCDXxi3dvaciIGm2IQo8UhKbaVXVeYgdX4=";
    };
  }.${stdenvNoCC.buildPlatform.system};

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [ ../go.mod ../go.sum ../cmd ../internal ../pkg ];
  };

  # Only used for its vendored modules (a fixed-output derivation).
  vendored = (buildGoModule {
    pname = "permaily-go-modules";
    version = "0";
    inherit src;
    vendorHash = "sha256-ua/w8D5vTAxur3Oq8sPdSiO9DF4Jc+pEGDWeJFA8kWk=";
  }).goModules;

  ldflags = lib.concatStringsSep " " [
    "-s"
    "-w"
    "-buildid="
    "-X github.com/permaily-org/permaily/internal/buildinfo.Commit=${rev}"
  ];
in
stdenvNoCC.mkDerivation {
  pname = subPackage;
  version = "0";
  inherit src;

  dontConfigure = true;
  dontFixup = true;

  buildPhase = ''
    runHook preBuild
    tar -xzf ${goToolchain} -C $TMPDIR
    export PATH=$TMPDIR/go/bin:$PATH
    export HOME=$TMPDIR GOCACHE=$TMPDIR/go-cache GOPATH=$TMPDIR/gopath
    export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS="-mod=vendor -trimpath"
    export CGO_ENABLED=0 GOOS=linux GOARCH=${goarch}
    cp -R ${vendored} vendor
    ${lib.optionalString (web != null) ''
      rm -rf internal/webui/dist
      cp -R ${web} internal/webui/dist
      chmod -R u+w internal/webui/dist
    ''}
    go build -ldflags "${ldflags}" -o "$TMPDIR/out/${subPackage}" ./cmd/${subPackage}
    runHook postBuild
  '';

  installPhase = ''
    install -D -m 0555 "$TMPDIR/out/${subPackage}" "$out/bin/${subPackage}"
  '';

  meta.mainProgram = subPackage;
}
