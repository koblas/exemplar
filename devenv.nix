{
  pkgs,
  lib,
  config,
  inputs,
  ...
}:

let
  # Go 1.27.2 fixes the stdlib vulnerabilities govulncheck reports in 1.27.1
  # (net/http, http2, crypto/tls, html/template, net/textproto). nixpkgs
  # channels still ship 1.27.1, so build the official source release. Drop
  # this override once go_1_27 is 1.27.2 there.
  go = pkgs.go_1_27.overrideAttrs (old: rec {
    version = "1.27.2";
    src = pkgs.fetchurl {
      url = "https://go.dev/dl/go${version}.src.tar.gz";
      sha256 = "03495da2ba64894d40f5c4992e49454fa78b50690604ff92b6afff5081b76e62"; # published on go.dev/dl
    };
  });

  # golangci-lint must be built with a Go whose export data it can read;
  # nixpkgs' 2.13.2 (x/tools 0.49) cannot read Go 1.27.2's. 2.14.0 (x/tools
  # 0.50) can. Matches the version pinned in .github/workflows/ci.yml.
  golangci-lint =
    (pkgs.golangci-lint.override {
      buildGo127Module = pkgs.buildGo127Module.override { inherit go; };
    }).overrideAttrs
      (old: {
        version = "2.14.0";
        src = pkgs.fetchFromGitHub {
          owner = "golangci";
          repo = "golangci-lint";
          tag = "v2.14.0";
          hash = "sha256-HATA7JKHwEouM+8jYZbQrkX7p4gut4IpyTvcBexu/4o=";
        };
        vendorHash = "sha256-ekP/zDhYMpMG+tYAyYHNfLOCt/JkxrXUw1jHUEfsM8k=";
        ldflags = [
          "-s"
          "-w"
          "-X main.version=2.14.0"
          "-X main.commit=v2.14.0"
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
    pkgs.ripgrep # engine grep tool (spec §6.5)
    pkgs.ast-grep # Lane 1 and compile attempts (spec §6.6, §8.6)
    pkgs.semgrep # Lane 1 and compile attempts: taint and metavariable rules
    pkgs.universal-ctags # exemplar-search symbols (spec §6.4.1)
  ];

  # Go is set up here rather than with languages.go: that module rebuilds
  # its editor tools with the chosen Go, and the packaged staticcheck
  # (go-tools 2026.2.1) cannot read Go 1.27.2's export data. These are the
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
  # Local Postgres for development; start it with `devenv up`. Migrations
  # create the vector extension themselves.
  services.postgres = {
    enable = true;
    package = pkgs.postgresql_16;
    extensions = extensions: [ extensions.pgvector ];
    listen_addresses = "127.0.0.1";
    initialDatabases = [ { name = "exemplar"; } ];
  };

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
