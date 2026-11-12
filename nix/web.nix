# Builds the SPA (web/) with bun. The output is embedded in permaily-enclave.
#
# node_modules is a fixed-output derivation: bun installs exactly bun.lock.
# Its hash differs per build platform because some dependencies ship native
# binaries (rolldown, typescript, tailwind), but the resulting dist/ is identical.
{ lib, stdenvNoCC, bun, stdenv }:

let
  # The web app imports the shared tokens from packages/ui by relative path,
  # so the source root is the repo root and the build runs in web/.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../packages/ui/colors.css
      ../web/package.json
      ../web/bun.lock
      ../web/index.html
      ../web/tsconfig.json
      ../web/tsconfig.app.json
      ../web/tsconfig.node.json
      ../web/vite.config.ts
      ../web/public
      ../web/src
    ];
  };
  sourceRoot = "source/web";

  nodeModules = stdenvNoCC.mkDerivation {
    pname = "permaily-web-node-modules";
    version = "0";
    inherit src sourceRoot;
    nativeBuildInputs = [ bun ];
    impureEnvVars = lib.fetchers.proxyImpureEnvVars;
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      export HOME=$TMPDIR
      bun install --frozen-lockfile --no-progress --ignore-scripts
      runHook postBuild
    '';
    installPhase = ''
      mkdir -p $out
      cp -R node_modules $out/
    '';
    dontFixup = true;
    outputHashMode = "recursive";
    outputHashAlgo = "sha256";
    outputHash = {
      x86_64-linux = "sha256-Er2BRc0ZiucXN1IrPeysAkiZqptfvDunB3Xxz0/hmMk=";
      aarch64-linux = "sha256-EonO5ePUi7fhkipe2TPJQs0pJlxjALRpGdRwcx5BJLI=";
    }.${stdenv.hostPlatform.system};
  };
in
stdenvNoCC.mkDerivation {
  pname = "permaily-web";
  version = "0";
  inherit src sourceRoot;
  nativeBuildInputs = [ bun ];
  dontConfigure = true;
  buildPhase = ''
    runHook preBuild
    export HOME=$TMPDIR
    cp -R ${nodeModules}/node_modules .
    chmod -R u+w node_modules
    # Same as `bun run build`, but calling the entry points directly: the
    # node_modules/.bin shims use #!/usr/bin/env, which the Nix sandbox lacks.
    bun --bun node_modules/typescript/bin/tsc -b
    bun --bun node_modules/vite/bin/vite.js build
    runHook postBuild
  '';
  installPhase = ''
    cp -R dist $out
  '';
}
