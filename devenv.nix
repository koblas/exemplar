{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

let
  # Go 1.27.3 fixes the stdlib vulnerabilities govulncheck reports in earlier
  # 1.27 releases (1.27.2: crypto/x509, GO-2026-6809; 1.27.1: net/http, http2,
  # crypto/tls, html/template, net/textproto). nixpkgs channels lag behind,
  # so build the official source release. Drop this override once go_1_27 is
  # 1.27.3 there.
  go = pkgs.go_1_27.overrideAttrs (old: rec {
    version = "1.27.3";
    src = pkgs.fetchurl {
      url = "https://go.dev/dl/go${version}.src.tar.gz";
      sha256 = "6e7749035cf360f06b2575d162ea79df9fda8ef671889e5c375f8e50777f26bb"; # published on go.dev/dl
    };
  });

  # golangci-lint must be built with a Go whose export data it can read;
  # nixpkgs' 2.13.2 (x/tools 0.49) cannot read Go 1.27's. 2.15.0 (x/tools
  # 0.52) can. Matches the version pinned in .github/workflows/ci.yml.
  golangci-lint =
    (pkgs.golangci-lint.override {
      buildGo127Module = pkgs.buildGo127Module.override { inherit go; };
    }).overrideAttrs
      (old: {
        version = "2.15.0";
        src = pkgs.fetchFromGitHub {
          owner = "golangci";
          repo = "golangci-lint";
          tag = "v2.15.0";
          hash = "sha256-m9PoJHescRuT9DiKsb6LL+lQlc7RKAWDiRyf8pNMffA=";
        };
        vendorHash = "sha256-gWbhZc/BJwuiwyTMaR1ShEBI1lo0bGLCXsdj5n91baw=";
        ldflags = [
          "-s"
          "-w"
          "-X main.version=2.15.0"
          "-X main.commit=v2.15.0"
          "-X main.date=1970-01-01T00:00:00Z"
        ];
      });
in
{
  # https://devenv.sh/basics/
  env.GREET = "devenv";

  # https://devenv.sh/packages/
  packages = [
    pkgs.git
    go
    pkgs.gopls
    pkgs.delve
    golangci-lint
    pkgs.ripgrep # engine grep tool
    pkgs.ast-grep # compiled rules and compile attempts
    pkgs.semgrep # compiled rules and compile attempts: taint and metavariable rules
    pkgs.universal-ctags # exemplar-search symbols
  ];

  # Go is set up here rather than with languages.go: that module rebuilds
  # its editor tools with the chosen Go, and the packaged staticcheck
  # (go-tools 2026.2.1) cannot read Go 1.27's export data. These are the
  # settings languages.go would make; gopls and delve are nixpkgs' builds.
  env.GOROOT = "${go}/share/go/";
  env.GOPATH = config.env.DEVENV_STATE + "/go";
  env.GOTOOLCHAIN = "local";

  # Learning-plane jobs in learning/ (embed, cluster, rank). uv manages the
  # project's own venv from learning/uv.lock; run `cd learning && uv sync`.
  languages.python = {
    enable = true;
    package = pkgs.python313;
    uv.enable = true;
  };

  # https://devenv.sh/processes/
  # processes.dev.exec = "${lib.getExe pkgs.watchexec} -n -- ls -la";

  # https://devenv.sh/services/
  # No Postgres service: tests start a pgvector container with testcontainers.

  # https://devenv.sh/scripts/
  scripts.hello.exec = ''
    echo hello from $GREET
  '';

  # https://devenv.sh/basics/
  enterShell = ''
    export PATH=$GOPATH/bin:$PATH
    hello         # Run scripts directly
    git --version # Use packages
  '';

  # https://devenv.sh/tasks/
  # tasks = {
  #   "myproj:setup".exec = "mytool build";
  #   "devenv:enterShell".after = [ "myproj:setup" ];
  # };

  # https://devenv.sh/tests/
  enterTest = ''
    echo "Running tests"
    git --version | grep --color=auto "${pkgs.git.version}"
  '';

  # https://devenv.sh/git-hooks/
  # git-hooks.hooks.shellcheck.enable = true;

  # See full reference at https://devenv.sh/reference/options/
}
