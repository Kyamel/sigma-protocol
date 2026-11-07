{
  description = "Sigma Protocol";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    flake-compat = {
      url = "github:NixOS/flake-compat";
      flake = false;
    };
  };

  outputs = { nixpkgs, ... }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
          tex = pkgs.texlive.combine {
            inherit (pkgs.texlive)
              scheme-medium titlesec latexmk totpages
              environ trimspaces framed zref hyperxmp ncctools comment ifmtarg preprint upquote
              libertine inconsolata newtx
              collection-langportuguese;
          };
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go gopls delve go-tools gcc
              git gnumake
              tex poppler-utils
              (python3.withPackages (ps: [ ps.matplotlib ]))
            ] ++ lib.optionals stdenv.isLinux [
              iproute2 tcpdump wireshark-cli
            ];
            GOTOOLCHAIN = "local";
          };
        });
    };
}
