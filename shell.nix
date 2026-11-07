let
  lock = builtins.fromJSON (builtins.readFile ./flake.lock);
  node = lock.nodes.${lock.nodes.root.inputs.flake-compat}.locked;
  compat = builtins.fetchTarball {
    url = "https://github.com/NixOS/flake-compat/archive/${node.rev}.tar.gz";
    sha256 = node.narHash;
  };
in
(import compat {
  # Inclui arquivos novos mesmo antes do git add; evita o filtro Git do compat.
  src = builtins.path {
    path = ./.;
    name = "sigma-source";
    filter = path: _type: builtins.baseNameOf path != ".git";
  };
}).shellNix
