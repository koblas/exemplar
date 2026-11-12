# Changelog

All notable changes to Exemplar are recorded here. release-please writes each
entry from the Conventional Commits merged to `main`; see
[CONTRIBUTING.md](CONTRIBUTING.md#releasing). Do not edit released entries by hand.

<!-- release-please inserts new entries above the first version heading. -->

## [0.14.0](https://github.com/koblas/exemplar/compare/v0.13.0...v0.14.0) (2026-11-12)


### Features

* **deploy:** install on EKS behind a shared ALB and Traefik, with Bedrock and RDS ([f51a632](https://github.com/koblas/exemplar/commit/f51a63264e625e659c0ede0e1f105913ee349987))
* **deploy:** take the Ingress host from EXEMPLAR_API_URL in the example overlays ([f30eee7](https://github.com/koblas/exemplar/commit/f30eee7daca1f8c6d72bea9259366ec8b7dcb7a5))
* **deploy:** wire Bedrock API key and region into the gateway ([f09b3cd](https://github.com/koblas/exemplar/commit/f09b3cd83d573009a48e4c6437840415f32637c3))


### Bug Fixes

* **deps:** bump Go to 1.27.4 for GO-2026-6859 ([1a359d7](https://github.com/koblas/exemplar/commit/1a359d7ca7b32ff155f8f4e2af5be726c00e3e33))
* **deps:** bump Go to 1.27.4 for GO-2026-6859 ([11983ba](https://github.com/koblas/exemplar/commit/11983bab031c9aa810a032352624a7b83b26ecb7))

## [0.13.0](https://github.com/koblas/exemplar/compare/v0.12.0...v0.13.0) (2026-11-08)


### Features

* **api:** setup at the public URL with state in Secrets ([fcce458](https://github.com/koblas/exemplar/commit/fcce458eaff916d9790ac74b99f04c543d2ff730))
* **api:** setup at the public URL with state in Secrets, no restart ([6e2df02](https://github.com/koblas/exemplar/commit/6e2df026fd9f34de12b0ee0ed88c4f903d8fbfcf))
* **deploy:** exemplar-api Role, token and declared Secrets for setup ([5f44799](https://github.com/koblas/exemplar/commit/5f44799ba519c2eefe6ea3ac95a9248e3464a54f))
* **ui:** restyle /setup and /ui/ with vendored Bulma 1.0.4 ([e93f684](https://github.com/koblas/exemplar/commit/e93f684dd5c2c9978f89f83adc047112acc3d72e))
* **ui:** restyle /setup and /ui/ with vendored Bulma 1.0.4 ([1ac9b87](https://github.com/koblas/exemplar/commit/1ac9b87abe1ca32b595f673169a767b679d6cfc1))


### Bug Fixes

* **api:** hold workloads until setup is done ([c881b3f](https://github.com/koblas/exemplar/commit/c881b3f31a8c1638d95dab9458defef15e229ca9))
* **deps:** bump brotli and klauspost/compress for GO-2026-6888, GO-2026-6865 ([030147f](https://github.com/koblas/exemplar/commit/030147f6b9645f38424e4954d111b65a79224cc2))

## [0.12.0](https://github.com/koblas/exemplar/compare/v0.11.1...v0.12.0) (2026-11-05)


### Features

* **setup:** show a done state with the next step ([f240168](https://github.com/koblas/exemplar/commit/f2401685047e8f7fabd583630fd3ba9880b684fe))

## [0.11.1](https://github.com/koblas/exemplar/compare/v0.11.0...v0.11.1) (2026-11-05)


### Bug Fixes

* **api:** let a personal-account config repo owner sign in ([74df0a9](https://github.com/koblas/exemplar/commit/74df0a9e7cd1ff9b12d554830eb2702b7e46e1f5))
* **api:** let a personal-account config repo owner sign in ([e16c451](https://github.com/koblas/exemplar/commit/e16c451daa70b2a27e984d57dd509171f61f4951))

## [0.11.0](https://github.com/koblas/exemplar/compare/v0.10.0...v0.11.0) (2026-11-04)


### Features

* **config:** exemplar:recommended preset, extends, disabled_rules, rule_settings ([73b6674](https://github.com/koblas/exemplar/commit/73b667438d73a42d83a9d07f8e6b03f85f557027))
* **config:** exemplar:recommended preset, extends, disabled_rules, rule_settings ([91f2a11](https://github.com/koblas/exemplar/commit/91f2a1155b9ee53505f0c064e0a352612a0894a8))
* **config:** starter config extends exemplar:recommended and enrols no repo ([a7f3c3d](https://github.com/koblas/exemplar/commit/a7f3c3d00456b6033b82cd8a543fbcc606a0bf68))
* **config:** starter config extends exemplar:recommended and enrols no repo ([b0788a8](https://github.com/koblas/exemplar/commit/b0788a8c4620835f322f6b00d82f055ad8a0ef9a))
* **eval:** defence evals ship with the binary ([ff04758](https://github.com/koblas/exemplar/commit/ff047584713256ad565defee9caf7ea68b404806))
* **eval:** defence evals ship with the binary ([43a8c08](https://github.com/koblas/exemplar/commit/43a8c0829f72202d2282fcdb5f35cffb3dacab26))
* **learn:** preset rules get owners and change through rule_settings ([6e4c911](https://github.com/koblas/exemplar/commit/6e4c9115059bb2bad0ba98b62caac5d8e4a6ffc0))
* **learn:** preset rules get owners and change through rule_settings ([f15f008](https://github.com/koblas/exemplar/commit/f15f0084772b81cb105726f7f0d127fcf46b9bf0))


### Bug Fixes

* **eval:** synthetic cases run under an eval profile ([39d75be](https://github.com/koblas/exemplar/commit/39d75be132d511a90be2c8f213e4c61ab9d7af8d))
* **eval:** synthetic cases run under an eval profile ([3c0f73e](https://github.com/koblas/exemplar/commit/3c0f73ed0217ba257b4525dfcd96c36219ab0620))

## [0.10.0](https://github.com/koblas/exemplar/compare/v0.9.2...v0.10.0) (2026-11-02)


### Features

* **release:** publish each release's Kustomize tree as a Flux OCI artifact ([16a1bff](https://github.com/koblas/exemplar/commit/16a1bfff9ab30db8bcb254c0a7d25f3ccad102c1))
* **release:** publish each release's Kustomize tree as a Flux OCI artifact ([e038000](https://github.com/koblas/exemplar/commit/e038000d7712cce75201e7b2557bdd3e58f9702b))

## [0.9.2](https://github.com/koblas/exemplar/compare/v0.9.1...v0.9.2) (2026-11-01)


### Bug Fixes

* **compiled:** offline Semgrep validation; faster make check ([b38f4bf](https://github.com/koblas/exemplar/commit/b38f4bf93fdd0b5b82bd25a02d053d71794214be))

## [0.9.1](https://github.com/koblas/exemplar/compare/v0.9.0...v0.9.1) (2026-11-01)


### Bug Fixes

* **setup:** close the remaining setup dead ends ([acfc716](https://github.com/koblas/exemplar/commit/acfc716b41b938850907815e1e5913fd42af731c))
* **setup:** close the remaining setup dead ends ([d41c91a](https://github.com/koblas/exemplar/commit/d41c91a55d27a7d6f6dc6f082d785a4a89ec86db))
* **setup:** recover when the App is renamed or deleted on GitHub ([9d11697](https://github.com/koblas/exemplar/commit/9d116970275f695d1934a905a0f9dc69fb012b15))
* **setup:** recover when the App is renamed or deleted on GitHub ([1b6e46e](https://github.com/koblas/exemplar/commit/1b6e46e0bc09015793b913f6d08a1808a5395429))

## [0.9.0](https://github.com/koblas/exemplar/compare/v0.8.1...v0.9.0) (2026-11-01)


### Features

* **setup:** generate the quickstart's Secrets and reconcile state on every load ([d972345](https://github.com/koblas/exemplar/commit/d972345144e1dae040016dde0fb0f0bb539e5a45))
* **setup:** generate the quickstart's Secrets and reconcile state on every load ([c407988](https://github.com/koblas/exemplar/commit/c407988f2b27a626a7d0c1c415015e4e18d8eed6))
* **setup:** walk GitHub App registration as three steps with actionable errors ([8d23afe](https://github.com/koblas/exemplar/commit/8d23afed487cdbeaf9f8422f1cd157ef2f4d07da))
* **setup:** walk GitHub App registration as three steps with actionable errors ([87cf0a2](https://github.com/koblas/exemplar/commit/87cf0a2cd13e847d30e09989486dc21fd0d0e7dc))

## [0.8.1](https://github.com/koblas/exemplar/compare/v0.8.0...v0.8.1) (2026-10-31)


### Bug Fixes

* **images:** Docker Hub login for werf; OCI labels; faster, clearer release PRs ([ad2ebf8](https://github.com/koblas/exemplar/commit/ad2ebf864eabd084ec937b9ac3f8ec5df0099b02))
* **images:** sign in to Docker Hub where werf looks for the credentials ([badac23](https://github.com/koblas/exemplar/commit/badac230f28d80ca6c68008405946b27d97f7095))

## [0.8.0](https://github.com/koblas/exemplar/compare/v0.7.0...v0.8.0) (2026-10-31)


### Features

* **images:** tag GHCR releases with &lt;version&gt; and &lt;x.y&gt; ([faf2f8a](https://github.com/koblas/exemplar/commit/faf2f8a2472edb382b47e4bd807ccee217edcba5))

## [0.7.0](https://github.com/koblas/exemplar/compare/v0.6.1...v0.7.0) (2026-10-31)


### ⚠ BREAKING CHANGES

* **images:** new releases publish the learning image only as koblas/exemplar-learning:<version>. The learning-<version> tags in koblas/exemplar from v0.6.0 and earlier stay; installations that pin digests keep working, but overlays should point exemplar-learning's newName at docker.io/koblas/exemplar-learning.

### Features

* **images:** publish the learning image to its own repository ([012b551](https://github.com/koblas/exemplar/commit/012b551f60773d6aec4f17ba558028349918e8a8))

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
