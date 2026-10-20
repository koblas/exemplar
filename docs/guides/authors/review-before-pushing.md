# How to review before pushing

`exemplar review` sends the diff of your working tree to exemplar-api, which reviews it on the pushed base commit with every lane and the verifier, as a PR review would, and prints what would have been posted. Nothing is posted anywhere.

## 1. Install the CLI

Download the archive for your platform from the [GitHub release](https://github.com/koblas/exemplar/releases) (linux amd64 and arm64, darwin arm64 and amd64, with `SHA256SUMS`), check its checksum, and put `exemplar` on your `PATH`. Or build it; it embeds the tree-sitter grammars, so it needs cgo and a C compiler:

```bash
CGO_ENABLED=1 go build -o exemplar ./cmd/exemplar
```

## 2. Sign in once

=== "GitHub"

    With the review App's client id (`client-id` in the `exemplar-github-app` Secret; your platform team can tell you, or set `EXEMPLAR_OAUTH2_GITHUB_CLIENT_ID`):

    ```bash
    exemplar login --api https://exemplar.acme.com --client-id <Client ID>
    ```

    Open the URL shown and enter the code. The App must have **Enable Device Flow** ticked.

=== "OpenID Connect"

    With the public CLI client your identity provider was given ([How to set up sign-in](../install/sign-in.md#openid-connect)):

    ```bash
    exemplar login --api https://exemplar.acme.com --oidc --oidc-issuer https://acme.okta.com --client-id <CLI client id>
    ```

    The ID token is refreshed when it expires.

The token and the API URL are saved readable only by you, and the token is never sent anywhere but exemplar-api. In CI, set `EXEMPLAR_TOKEN` and `EXEMPLAR_API_URL` instead of signing in.

## 3. Review

From a clone whose base branch is pushed:

```bash
exemplar review
```

It diffs tracked changes and untracked files that aren't ignored against the merge-base with `--base` (origin's default branch by default), waits for the result (up to `--wait`, 15 minutes), and prints each finding as the comment would read. It exits 1 when there are findings, 0 with `--exit-zero`. `--format sarif` writes SARIF for tools that read it; `--repo` overrides the `owner/name` taken from the origin remote.

You must be able to read the repository on GitHub, and the repository must be enabled and eligible in `exemplar-config`. Each caller is rate-limited and held to a daily budget; only you see your result.

## What it can't do

It reviews the diff on the base commit it has, so a change that depends on an unpushed base commit is reviewed against the wrong tree: push the base first. It does not post, approve, or affect any PR, and findings from it don't count as feedback for rules.
