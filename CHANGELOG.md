# Changelog

All notable changes to Exemplar are recorded here. release-please writes each
entry from the Conventional Commits merged to `main`; see
[CONTRIBUTING.md](CONTRIBUTING.md#releasing). Do not edit released entries by hand.

<!-- release-please inserts new entries above the first version heading. -->

## [0.2.1](https://github.com/koblas/exemplar/compare/v0.2.0...v0.2.1) (2026-10-22)


### Bug Fixes

* **engine:** install ripgrep for the agents' grep tool ([aa97ca3](https://github.com/koblas/exemplar/commit/aa97ca37fad99366dcb6ce003e3c055b82b6d332))

## [0.2.0](https://github.com/koblas/exemplar/compare/v0.1.0...v0.2.0) (2026-10-22)


### Features

* **examples:** language-agnostic starter rules ([f5a7ebc](https://github.com/koblas/exemplar/commit/f5a7ebc6e4107502a228ecc5f842b5ba053cc851))


### Dependencies

* build with Go 1.27.3 ([9aa2f65](https://github.com/koblas/exemplar/commit/9aa2f6592d2420a768c7c95edea4f22f020c5a6e))

## 0.1.0 (2026-10-20)


### ⚠ BREAKING CHANGES

* **gates:** PRs that touch these files are no longer auto-approved.
* **gates:** native_approve repos whose branches require approvals but keep stale approvals are no longer auto-approved (stale_approvals_not_dismissed_on_push).
* **gates:** a PR that changes any file excluded from review, including a plain deletion, is no longer auto-approved.
* **learn:** `exemplar-learn evals` requires --config. Overlays that patch the evals CronJob must keep the new argument and init container.
* **worker:** config CI's Auth0 client needs evaluate:<owner>/<repo> (or evaluate:<owner>/*) for every repo its eval cases reference, not only exemplar-config.
* **api:** unlinked OIDC users sign in as oidc:<username>. They cannot run rule-owner actions, and their feedback counts as an observer's. Sessions issued before the upgrade keep the bare login until they expire.
* **gates:** an approval from an account without write access no longer satisfies exemplar/merge-policy.

### Features

* **api:** setup mode registers the GitHub App from a manifest ([96798ee](https://github.com/koblas/exemplar/commit/96798ee561f0f337bd94dd5b2b150ef597bc1860))
* **deploy:** add a quickstart overlay for trial installs ([ec3982c](https://github.com/koblas/exemplar/commit/ec3982c32721412063b8d43b8a5e6923d191612c))
* **deploy:** two settings, App values in one Secret, config sync as the App ([d0d1e53](https://github.com/koblas/exemplar/commit/d0d1e534ec82a19a0580692a6c3571af6e4bbd33))
* derive internal keys from one root key ([0fd6c76](https://github.com/koblas/exemplar/commit/0fd6c764831cf1e821771f47c9ac99109d6cef97))
* **learn:** recheck comments mined before ingest checked the commenter ([140cff7](https://github.com/koblas/exemplar/commit/140cff7892d91b0ee89b06512eab01417782373f))
* make a first install short and checkable (setup mode, root key, two settings) ([b073970](https://github.com/koblas/exemplar/commit/b0739704bfdfc6ba5222c0a9f5adcaa5b0f239d2))
* reject placeholder settings at startup ([ec44323](https://github.com/koblas/exemplar/commit/ec44323ee12b2ebf7703b6083f34d0840bf867fb))
* **web:** /ui/setup checks the GitHub side of an installation ([7510fe9](https://github.com/koblas/exemplar/commit/7510fe9c533ba477e98beb3b2999b6b71fb79e81))
* **worker:** state the human-review verdict on the check run ([1015013](https://github.com/koblas/exemplar/commit/1015013e1bf70aec8369ef1a16777717796e8665))
* **worker:** state the human-review verdict on the check run ([1f54642](https://github.com/koblas/exemplar/commit/1f54642fd654e2bbb502725c14dd64c26a651729))


### Bug Fixes

* **api:** require repo read access on feedback links ([868c0e3](https://github.com/koblas/exemplar/commit/868c0e33920efd048771fe8467e371e0a3475c94))
* **api:** sign in OIDC users without a connected GitHub account as oidc:&lt;username&gt; ([eff3fbb](https://github.com/koblas/exemplar/commit/eff3fbbc4668dd78af928748e9dfd8b3b6ceaf05))
* **cli:** strip control characters from model text ([fc38e07](https://github.com/koblas/exemplar/commit/fc38e076361e474d4b258618e06b191752825e62))
* clone from GitHub Enterprise Server when EXEMPLAR_GH_WEB_URL is set ([466afd0](https://github.com/koblas/exemplar/commit/466afd0c9c80d6d46cb0e004324e7a378a9638a0))
* close the security hunt's remaining open items ([1a78c1d](https://github.com/koblas/exemplar/commit/1a78c1d4e6eb97a6f7e04c8f5142d5e579ec2096))
* **compiled:** raise mode eval's engine data limit to 4 GiB ([a7a3fc1](https://github.com/koblas/exemplar/commit/a7a3fc19e41de489b8a4eba515ba78d80aaf9643))
* **compiled:** raise mode eval's engine data limit to 4 GiB ([9a3a5fe](https://github.com/koblas/exemplar/commit/9a3a5fec16b2e03c3578eaa6be4a983e9289b7b6))
* **compiled:** run Semgrep with one job under data limits ([cb791c0](https://github.com/koblas/exemplar/commit/cb791c0f16c0a1063f7680a782f1dcc62ef2b778))
* **compiled:** stop ast-grep loading a PR's sgconfig.yml ([6c13233](https://github.com/koblas/exemplar/commit/6c1323360254b12892f1bef231324f42744cda70))
* **deploy:** wire GHES URLs and make the exports app mount optional ([1a6d129](https://github.com/koblas/exemplar/commit/1a6d129627f202a95377f75b23e26ba5490e4b7e))
* **engine:** bind verifier evidence to the finding it confirms ([9c3fe16](https://github.com/koblas/exemplar/commit/9c3fe16a0f179d91e6efe342caf98274b6999de0))
* **engine:** keep excluded paths out of the agents' tools ([234c8fb](https://github.com/koblas/exemplar/commit/234c8fb5856e97c22272dccd3dc3b54b7d25f152))
* **engine:** never judge security or blocking findings human duplicates ([c916f30](https://github.com/koblas/exemplar/commit/c916f308eedc2da113cfe40fe80c418658067575))
* **gates:** count merge-policy approvals only from people with write access ([eb3ce4b](https://github.com/koblas/exemplar/commit/eb3ce4bae63ee1a704a9b545b1015d0615630759))
* **gates:** denylist build and dev-environment files for auto-approve ([1b529df](https://github.com/koblas/exemplar/commit/1b529df8d93c2d0e511bde67f5de3c89be20b8a7))
* **gates:** never auto-approve a PR that changes files excluded from review ([788f675](https://github.com/koblas/exemplar/commit/788f675928e364030f220891291f728fd08e0821))
* **gates:** require stale approvals to be dismissed on push for native_approve ([eea12f0](https://github.com/koblas/exemplar/commit/eea12f08a2c7147fa254fd1f49263ca78a4049f2))
* GHES wiring and clones, optional exports app, migration lock ([dd12029](https://github.com/koblas/exemplar/commit/dd12029d852d369d49219bd2fc2a749883998505))
* **gh:** merge only the app's own commits from config-writer branches ([fbf40fe](https://github.com/koblas/exemplar/commit/fbf40febef240f44402f0c13c7f60668c201a8a1))
* **gh:** refuse poster tokens on the config repo ([512a238](https://github.com/koblas/exemplar/commit/512a238a353c9421e4914f87f98078192463301a))
* **install:** first-run fixes and an Ingress quickstart ([e209542](https://github.com/koblas/exemplar/commit/e209542e5ceb10ee10a5405564273a6d70be44c6))
* **install:** skip credential-less providers, set up on user accounts, add Ingress quickstart ([edfd50c](https://github.com/koblas/exemplar/commit/edfd50ce83da80464a23f809a2974703b4b715ed))
* **learn:** mine review comments only from the repo's reviewers ([9a57d39](https://github.com/koblas/exemplar/commit/9a57d39670c3a8d9ef74aa80bb7b5bf3f5f362b4))
* **learn:** re-check repo eligibility before eval generation reads a repo ([d84526f](https://github.com/koblas/exemplar/commit/d84526f983322a9418c0b0c2c98d647386cf9ead))
* **learn:** re-check repo eligibility before extraction ([a8f7220](https://github.com/koblas/exemplar/commit/a8f7220a1e3d14a428c68b1272511c87c029a798))
* **learn:** retry extraction for repos that become eligible again ([a7cf713](https://github.com/koblas/exemplar/commit/a7cf71386c6191d15832dda2d1bfca9c69dc0a74))
* **learn:** skip export when no exports app is configured ([22ae2dd](https://github.com/koblas/exemplar/commit/22ae2dd8e6605d36797c61c31272b842d00c06af))
* **pipeline:** neutralize fences, mentions and HTML in model-written messages ([bc86f7c](https://github.com/koblas/exemplar/commit/bc86f7ceb9af6118ae7b367ab9dc97e0be376538))
* security hunt fixes (1 Critical, 1 High, 8 Medium) ([51da0b3](https://github.com/koblas/exemplar/commit/51da0b38983d0c9bf70b796b8959108cdd786b69))
* security hunt follow-ups ([a538f67](https://github.com/koblas/exemplar/commit/a538f676b219ad9faa8f5c569413f5510fd3f75b))
* **setup:** refuse requests for other hosts on the setup listener ([cc1984b](https://github.com/koblas/exemplar/commit/cc1984b8f50629c7e87490fad32878730315b84c))
* **store:** serialize migrations with a Postgres advisory lock ([f9cc047](https://github.com/koblas/exemplar/commit/f9cc047a125220ee3cdb05cfdfd18fe77691ad07))
* **web:** show candidate text only to readers of every source repo ([f27945e](https://github.com/koblas/exemplar/commit/f27945e08af4e88ba675483a70a1f26302fa2feb))
* **worker:** cap memory and CPU for mode eval's unreviewed compiled rules ([2bf39f1](https://github.com/koblas/exemplar/commit/2bf39f1b70c54e8b51d211da4de89269ef3391f6))
* **worker:** run mode-eval cases only on repos the requester can read ([bde1d5f](https://github.com/koblas/exemplar/commit/bde1d5fef83f3331412d8a1ed18fd9704425748e))
* **worker:** run playground compiled drafts under mode eval's limits ([5bdab41](https://github.com/koblas/exemplar/commit/5bdab41168094909c0f1ec4e3b0e393c09947dd9))

## 0.0.0 (2026-10-16)

Before the first release. Changes up to here are in the git history.
