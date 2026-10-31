# Changelog

All notable changes to Exemplar are recorded here. release-please writes each
entry from the Conventional Commits merged to `main`; see
[CONTRIBUTING.md](CONTRIBUTING.md#releasing). Do not edit released entries by hand.

<!-- release-please inserts new entries above the first version heading. -->

## [0.6.1](https://github.com/koblas/exemplar/compare/v0.6.0...v0.6.1) (2026-10-31)


### Bug Fixes

* **gh:** accept a client without a transport when completing a manifest ([022a64d](https://github.com/koblas/exemplar/commit/022a64d10a07466557d511e40ce0bd1e5a54c174))
* **gh:** accept a client without a transport when completing a manifest ([1324d41](https://github.com/koblas/exemplar/commit/1324d41e7c0f267a8bcc65f2d33a526d8973eb0c))

## [0.6.0](https://github.com/koblas/exemplar/compare/v0.5.0...v0.6.0) (2026-10-25)


### ⚠ BREAKING CHANGES

* **worker:** the worker's args gain --runner-quota=8 at args/8, and serve refuses a quota with --launcher=in-process. An overlay that patches args/7 to --launcher=in-process must also patch args/8 to --runner-quota=0, as the quickstart overlays now do, or the worker exits at startup.

### Features

* **deploy:** GKE Autopilot overlay ([b0d3123](https://github.com/koblas/exemplar/commit/b0d31232b39949ec525842a6caad5e5f6f1fd5f9))
* **deploy:** GKE Autopilot overlay ([f47a580](https://github.com/koblas/exemplar/commit/f47a5802070ed3851e05b5cfd5e56cbfcdb676d0))
* **deploy:** pgbouncer component and runner pooled database URL ([7853bfe](https://github.com/koblas/exemplar/commit/7853bfea482628df8e86ba857dfcf30e2926dbc5))
* **deploy:** pgbouncer component and runner pooled database URL ([c584279](https://github.com/koblas/exemplar/commit/c5842797ef7a09c86b40c469c50913646603a711))
* **examples:** end-to-end demo kit and runbook ([971d0c8](https://github.com/koblas/exemplar/commit/971d0c8fc1ea35e5aa288c48fb42211605f01481))
* **examples:** end-to-end demo kit and runbook ([8323945](https://github.com/koblas/exemplar/commit/832394587bf6155854957271e15acc0f2b71c3ce))
* **learning:** embed extractions through a Voyage route in the gateway ([0b4bffb](https://github.com/koblas/exemplar/commit/0b4bffbe2ee071d36be85e56305c1bf64d8c2326))
* **learning:** embed extractions through a Voyage route in the gateway ([f3652a5](https://github.com/koblas/exemplar/commit/f3652a5a082b8e210b580fa8c758c7def863f492))
* **learning:** re-embed on embedding model change and cost Voyage calls ([cbfc00d](https://github.com/koblas/exemplar/commit/cbfc00d64cfcc9cc18a08bdd8bea1d61484259d1))
* **learning:** re-embed on embedding model change and cost Voyage calls ([2929357](https://github.com/koblas/exemplar/commit/2929357a2fbc052cce57427ce5f0d7abffa08818))
* **llm:** add an OpenAI provider as a second model family ([ea8ba31](https://github.com/koblas/exemplar/commit/ea8ba31f93fc24bad02e68e405f0afce371fff01))
* **llm:** add an OpenAI provider as a second model family ([fd2523c](https://github.com/koblas/exemplar/commit/fd2523c319a51be7521d2e6b61335d97a190db52))
* **worker:** per-review checkout volume sized per job ([ed6acc5](https://github.com/koblas/exemplar/commit/ed6acc5dc686cb46e66682056083dd33c61d6f4e))
* **worker:** per-review checkout volume sized per job ([e14f267](https://github.com/koblas/exemplar/commit/e14f26711082ad393a1ae5fcc9eceb3238684bcd))
* **worker:** run the baseline reviewer live on sampled heads ([308bf2f](https://github.com/koblas/exemplar/commit/308bf2f0c50c333ce360df394a612c067b403705))
* **worker:** run the baseline reviewer live on sampled heads ([8a4aad8](https://github.com/koblas/exemplar/commit/8a4aad871fcef46b4231091993e5920a786c78b5))
* **worker:** runner quota and queue metrics ([222a287](https://github.com/koblas/exemplar/commit/222a28759b43e74b75c4216535e59206de4a1ac7))
* **worker:** runner quota and queue metrics ([f6f6ccf](https://github.com/koblas/exemplar/commit/f6f6ccf1187a723ea88cb150c3342c41a85d25e2))


### Bug Fixes

* **gh:** take over a head's in-progress check run instead of adding one ([c514c2f](https://github.com/koblas/exemplar/commit/c514c2f7e40ea27a0c5c79d20c260a5e75e490d1))
* one check run per head on retries; UI eFP flag at the configured bar ([daf3416](https://github.com/koblas/exemplar/commit/daf341684be29e2c2b0a231a74b666b1d8a479d4))
* **web:** flag rules over the configured eFP bar ([fdadd76](https://github.com/koblas/exemplar/commit/fdadd76a556ca2186d185a6924fe7798b61078b7))
* **worker:** expire runner claims by the later of two clocks ([c0b1e24](https://github.com/koblas/exemplar/commit/c0b1e2460535db0066ebf524370f918a5687e71f))
* **worker:** never run two reviews of one PR head at once ([fdf0014](https://github.com/koblas/exemplar/commit/fdf0014e1feacbb7d24e49a0e946e2ad19d5908f))
* **worker:** size the sim's pool so overlaps cannot exhaust it ([1c6343a](https://github.com/koblas/exemplar/commit/1c6343a9bf390c9d6d956f91df4cf8186cea7156))
* **worker:** size the sim's pool so overlaps cannot exhaust it ([8d4c3df](https://github.com/koblas/exemplar/commit/8d4c3df4ae8d38249df3faee354b5e0226b6271a))

## [0.5.0](https://github.com/koblas/exemplar/compare/v0.4.0...v0.5.0) (2026-10-25)


### Features

* **engine:** group a rule's occurrences into one comment ([5196711](https://github.com/koblas/exemplar/commit/5196711e8ec6a1f8f7fca6ccc76cdec4b22a0945))
* **engine:** group a rule's occurrences into one comment ([c2fca06](https://github.com/koblas/exemplar/commit/c2fca063698c8a86ca107f08833593a938f358e2))
* **engine:** verifier quorum for the bug lane ([1ea8f8d](https://github.com/koblas/exemplar/commit/1ea8f8db24e7505900c72135521fccc6cc2cd267))
* **engine:** verifier quorum for the bug lane ([6c1ef0d](https://github.com/koblas/exemplar/commit/6c1ef0d94b9d788c5adad695818d15029f172962))
* **learn:** advisory lock in every scheduled job ([b6fe537](https://github.com/koblas/exemplar/commit/b6fe53789c88ceac9c7bef03cdddf4170d12d7fd))
* **learn:** advisory lock in every scheduled job ([3a4d371](https://github.com/koblas/exemplar/commit/3a4d371a5465e55d5d1ef565655002ecc9260ac4))
* **learn:** owner-written seeds through the candidate pipeline ([d035f51](https://github.com/koblas/exemplar/commit/d035f5180e121cb358b55c804671336e493c9447))
* **learn:** owner-written seeds through the candidate pipeline ([067480b](https://github.com/koblas/exemplar/commit/067480bddebecf33659f94b02f2ea0bc90e056e3))
* **learn:** settle candidate PRs closed without merging ([23825ec](https://github.com/koblas/exemplar/commit/23825eca28a435d1e45af0d22f4aee9062fd2803))
* **learn:** settle candidate PRs closed without merging ([50af482](https://github.com/koblas/exemplar/commit/50af482fad01f7a88562e4c02575b3eee2a34a24))


### Bug Fixes

* **worker:** close out a review whose job failed its last attempt ([b13cd0f](https://github.com/koblas/exemplar/commit/b13cd0fbbe9459d64ac44c77943f22a10203b3dd))

## [0.4.0](https://github.com/koblas/exemplar/compare/v0.3.0...v0.4.0) (2026-10-25)


### Features

* **engine:** answer find_references and path_history from the checkout without search ([dd8ff49](https://github.com/koblas/exemplar/commit/dd8ff495cdec70fd1d966fbe1ffe493c95315437))
* **engine:** answer find_references and path_history from the checkout without search ([323b86a](https://github.com/koblas/exemplar/commit/323b86acde147ef454ae1c1fc84144653b44a819))
* **gates:** a clean run ignores suggestion findings ([377c45f](https://github.com/koblas/exemplar/commit/377c45fa4187239d62c52099acc236c4693cb827))
* **gates:** a clean run ignores suggestion findings ([8460455](https://github.com/koblas/exemplar/commit/8460455a19e0b9d079e41bfe38d158c7ec527419))
* **gates:** same-family verification stops gating once the verifier is calibrated ([313c1ee](https://github.com/koblas/exemplar/commit/313c1ee2eb63c9e1ab7c79b27f2ed025b2c2238f))
* **gates:** same-family verification stops gating once the verifier is calibrated ([1b6459b](https://github.com/koblas/exemplar/commit/1b6459beefc7ae42a9204418162a571c273efabb))
* **pipeline:** say what a review without search did not check ([1eef231](https://github.com/koblas/exemplar/commit/1eef2316d66806ff91ce609e6b8d0ffff71956ba))
* **pipeline:** say what a review without search did not check ([e6b95df](https://github.com/koblas/exemplar/commit/e6b95dffdbcfef31d810cf7493fb122598c8a804))
* **worker:** cap review rounds per PR with a summary comment ([1071105](https://github.com/koblas/exemplar/commit/1071105a3d79a5488c4a7b8234fa49f977f43db8))
* **worker:** cap review rounds per PR with a summary comment ([eda61d9](https://github.com/koblas/exemplar/commit/eda61d93bf13bcf6274b3db175eea59e0dd07368))


### Bug Fixes

* **engine:** turn off git's background maintenance in checkouts and fixtures ([21871f4](https://github.com/koblas/exemplar/commit/21871f4b72aaead6fff86e3aba0767870b3da90f))
* **synth:** skip files that vanish while copying a cached fixture ([0a597db](https://github.com/koblas/exemplar/commit/0a597db1498006bd94c91928a265d88571efd72b))
* **synth:** skip files that vanish while copying a cached fixture ([3a2d309](https://github.com/koblas/exemplar/commit/3a2d3097b73961e307eadb8d75adc0dd871babcc))
* **worker:** post findings whose review never reached GitHub ([bc852a6](https://github.com/koblas/exemplar/commit/bc852a6a6b80c011f535047c10da6a8ad042fd3b))

## [0.3.0](https://github.com/koblas/exemplar/compare/v0.2.2...v0.3.0) (2026-10-24)


### Features

* **worker:** hand runners single-use token claims instead of the App key ([4950556](https://github.com/koblas/exemplar/commit/4950556b7733d29941ffad62452063ab8b958b65))
* **worker:** hand runners single-use token claims instead of the App key ([5b634b4](https://github.com/koblas/exemplar/commit/5b634b423bb6ac5d219c262dcd50e82cdc60212c))
* **worker:** run reviews as Kubernetes Jobs with stored config snapshots ([6ec7743](https://github.com/koblas/exemplar/commit/6ec7743baa3ad107ea75d5a312c4ca52a8012ead))
* **worker:** run reviews as Kubernetes Jobs with stored config snapshots ([e67589c](https://github.com/koblas/exemplar/commit/e67589c2607d7e8f9ff2a6a47e8595784adbebb6))

## [0.2.2](https://github.com/koblas/exemplar/compare/v0.2.1...v0.2.2) (2026-10-22)


### Bug Fixes

* **redact:** make New safe to call concurrently ([00bf9df](https://github.com/koblas/exemplar/commit/00bf9dfba399965bae189ea6d1a157faa79dbcdb))

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
