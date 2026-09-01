{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

{

  packages = [
    pkgs.delve
    pkgs.golangci-lint
    pkgs.git
  ];

  languages.go = {
    enable = true;
    version = "1.27.0";
    enableHardeningWorkaround = true;
    delve.enable = true;
    lsp.enable = true;

  };
  overlays = [
    (final: prev: {
      go-tools = prev.go-tools.overrideAttrs (old: {
        doCheck = false;
      });
    })
  ];
}
