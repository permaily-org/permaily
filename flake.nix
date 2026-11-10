{
  description = "Permaily: verifiable email access broker running in AWS Nitro Enclaves";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    nitro-util = {
      url = "github:monzo/aws-nitro-util";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nitro-util }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
      # The commit is embedded in the binary, so the PCRs identify a commit.
      # Builds from a dirty tree get a "-dirty" revision and different PCRs.
      rev = self.rev or self.dirtyRev or "unknown";
    in
    {
      packages = forAll (system: pkgs:
        let
          web = pkgs.callPackage ./nix/web.nix { };
          goBin = args: pkgs.callPackage ./nix/go.nix ({ inherit rev; } // args);
          enclaveBin = goBin { subPackage = "permaily-enclave"; goarch = "arm64"; inherit web; };
          mkEif = { acmeDirectory, staging ? false }: pkgs.callPackage ./nix/eif.nix {
            nitro = nitro-util.lib.${system};
            inherit enclaveBin acmeDirectory staging;
          };
        in
        rec {
          inherit web;
          permaily-enclave = enclaveBin;
          # The parent EC2 instance is Graviton (arm64).
          permaily-host = goBin { subPackage = "permaily-host"; goarch = "arm64"; };
          # The verifier runs on the machine of whoever verifies.
          permaily-verify = goBin { subPackage = "permaily-verify"; goarch = pkgs.go.GOARCH; };
          # Production image: Let's Encrypt production. This is what releases publish.
          eif = mkEif { acmeDirectory = "https://acme-v02.api.letsencrypt.org/directory"; };
          # The same code as a deployment of its own, for tests: Let's Encrypt
          # staging, its own storage, and a root secret that only images built
          # by the staging workflow can receive (deploy/enclave.json).
          eif-staging = mkEif { acmeDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"; staging = true; };
          default = eif;
        });

      devShells = forAll (system: pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [ go gopls golangci-lint govulncheck gnumake bun nodejs_24 opentofu awscli2 jq ];
        };
      });
    };
}
