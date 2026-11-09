# Builds one Go command as a static binary for linux/<goarch>.
#
# Cross-compilation is done by the Go toolchain itself (GOARCH), not by a Nix
# cross toolchain, so the binary is byte-identical whether it is built on an
# x86_64 or an aarch64 machine.
{ lib, buildGoModule, rev, subPackage, goarch, web ? null }:

buildGoModule {
  pname = subPackage;
  version = "0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [ ../go.mod ../go.sum ../cmd ../internal ../pkg ];
  };
  vendorHash = "sha256-fDlCz9E4Rq0US+iiBI+hMgKEXmdBFADE9Jqa/MJI20Q=";

  subPackages = [ "cmd/${subPackage}" ];
  env.CGO_ENABLED = "0";
  ldflags = [
    "-s"
    "-w"
    "-X github.com/permaily-org/permaily/internal/buildinfo.Commit=${rev}"
  ];

  preBuild = ''
    export GOOS=linux GOARCH=${goarch}
  '' + lib.optionalString (web != null) ''
    rm -rf internal/webui/dist
    cp -R ${web} internal/webui/dist
    chmod -R u+w internal/webui/dist
  '';

  doCheck = false;

  # Go puts cross-compiled binaries in bin/<goos>_<goarch>/.
  postInstall = ''
    if [ -d "$out/bin/linux_${goarch}" ]; then
      mv "$out/bin/linux_${goarch}/"* "$out/bin/"
      rmdir "$out/bin/linux_${goarch}"
    fi
  '';

  meta.mainProgram = subPackage;
}
