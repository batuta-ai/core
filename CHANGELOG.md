# Changelog

## [1.1.0-beta.15](https://github.com/batuta-ai/core/compare/v1.1.0-beta.14...v1.1.0-beta.15) (2026-09-06)


### Features

* **routing:** per-task blocks in a plan's Decisions and context ([cb868ff](https://github.com/batuta-ai/core/commit/cb868ffa216cd1e8e8a77410c31c402a75d74a3f))
* **routing:** per-task blocks in a plan's Decisions and context ([db8031a](https://github.com/batuta-ai/core/commit/db8031a0ce957643c8b209a6abaa83be6f6a8fff)), closes [#53](https://github.com/batuta-ai/core/issues/53)

## [1.1.0-beta.14](https://github.com/batuta-ai/core/compare/v1.1.0-beta.13...v1.1.0-beta.14) (2026-09-06)


### Bug Fixes

* **loop:** executor and verifier sessions run with commit.gpgsign=false ([1ca5173](https://github.com/batuta-ai/core/commit/1ca51732f962e2cf08cecb41d5d1385c5f608cb9))
* **loop:** executor and verifier sessions run with commit.gpgsign=false ([05b8a18](https://github.com/batuta-ai/core/commit/05b8a18cfef56f557e9443be7b695f4bff7edd76)), closes [#49](https://github.com/batuta-ai/core/issues/49)

## [1.1.0-beta.13](https://github.com/batuta-ai/core/compare/v1.1.0-beta.12...v1.1.0-beta.13) (2026-09-06)


### Features

* batuta gate proofs runs one proof per criterion ([848fc5b](https://github.com/batuta-ai/core/commit/848fc5bb0fe1a9e65c26806be10371df220f0268))
* batuta gate scope compares changed paths against a Scope list ([d8e0db0](https://github.com/batuta-ai/core/commit/d8e0db0335ad25b5d6bff4439f21f1d753d550f8))
* batuta gate tests runs the test command and prints its verdict ([7211ccd](https://github.com/batuta-ai/core/commit/7211ccd1a9a2510f7a8b4a6db6371a76e88346f9))
* batuta gate verifier parses TASK lines from stdin ([c75790e](https://github.com/batuta-ai/core/commit/c75790e4caecb8a4690a083479b4945849ef6d8b))
* capabilities advertises gate; usage and docs list the five gates ([98cdfd5](https://github.com/batuta-ai/core/commit/98cdfd57f6ae8b45f221b02b6f41309cd28a36b0))
* **cli:** batuta gate subcommands, plan directories, test hygiene and doctor ([#29](https://github.com/batuta-ai/core/issues/29), [#35](https://github.com/batuta-ai/core/issues/35), [#37](https://github.com/batuta-ai/core/issues/37), [#39](https://github.com/batuta-ai/core/issues/39), [#41](https://github.com/batuta-ai/core/issues/41), [#43](https://github.com/batuta-ai/core/issues/43)) ([d380f35](https://github.com/batuta-ai/core/commit/d380f35759276223ab13081bf7b966e78e876758))
* doctor tells managed state apart from a dirty tree ([d07270a](https://github.com/batuta-ai/core/commit/d07270a344be5b4b9c23bbea500c3f3dba91920e))
* plans live under .batuta/plans and move to .batuta/plans/done when f ([31e61c3](https://github.com/batuta-ai/core/commit/31e61c343adb100b78aaede7d5a59f975de9801b))


### Bug Fixes

* **loop:** accept .batuta/plans/&lt;slug&gt;.md as the plan argument ([45c9c09](https://github.com/batuta-ai/core/commit/45c9c090e943c61aa0b6f9d9db3c7cae6068efcf))

## [1.1.0-beta.12](https://github.com/batuta-ai/core/compare/v1.1.0-beta.11...v1.1.0-beta.12) (2026-09-06)


### Features

* batuta loop --dashboard --watch redraws the panel until the delivery ([01f7816](https://github.com/batuta-ai/core/commit/01f78160bc3653590f814cec0464282837fe2872))
* gate 3 names a criterion reported DONE whose proof failed ([a557ad6](https://github.com/batuta-ai/core/commit/a557ad60dea7900aa86afaf91b36cbc10080b92d))
* **loop:** live dashboard and per-criterion progress protocol ([#34](https://github.com/batuta-ai/core/issues/34)) ([f09b1a6](https://github.com/batuta-ai/core/commit/f09b1a6db61414ca8c7a0a08b002eb72bd9e703d))
* render the live panel from a delivery's journal ([69432b4](https://github.com/batuta-ai/core/commit/69432b4677a0cc2d5b2527b526bb327bb27790b1))


### Bug Fixes

* **executor:** parse BATUTA-PROGRESS lines on stderr too ([5d837d5](https://github.com/batuta-ai/core/commit/5d837d56f8797cd98da2146051a1ecd816286a3f))

## [1.1.0-beta.11](https://github.com/batuta-ai/core/compare/v1.1.0-beta.10...v1.1.0-beta.11) (2026-09-06)


### Bug Fixes

* **gates:** the verifier honours the proofs the conductor already ran ([#44](https://github.com/batuta-ai/core/issues/44)) ([160858a](https://github.com/batuta-ai/core/commit/160858ab6e9446fed9605898d3a7a5e58380b9e4))

## [1.1.0-beta.10](https://github.com/batuta-ai/core/compare/v1.1.0-beta.9...v1.1.0-beta.10) (2026-09-06)


### Features

* **cli:** batuta gate tree, the tree gate as a standalone subcommand ([#38](https://github.com/batuta-ai/core/issues/38)) ([75bb633](https://github.com/batuta-ai/core/commit/75bb633aedf2b4335f790ce6662708d0ddf4ca2f))

## [1.1.0-beta.9](https://github.com/batuta-ai/core/compare/v1.1.0-beta.8...v1.1.0-beta.9) (2026-09-06)


### Bug Fixes

* **release:** build the batuta binary on windows again ([#32](https://github.com/batuta-ai/core/issues/32)) ([29d3422](https://github.com/batuta-ai/core/commit/29d34222e479ba948d86632bb8323326b2504b02))

## [1.1.0-beta.8](https://github.com/batuta-ai/core/compare/v1.1.0-beta.7...v1.1.0-beta.8) (2026-09-06)


### Features

* **loop:** batuta loop, the mechanical conductor over the delivery graph ([#30](https://github.com/batuta-ai/core/issues/30)) ([50d01ca](https://github.com/batuta-ai/core/commit/50d01ca9aa7c874fc85b821064e92ffb263971b8)), closes [#18](https://github.com/batuta-ai/core/issues/18)

## [1.1.0-beta.7](https://github.com/batuta-ai/core/compare/v1.1.0-beta.6...v1.1.0-beta.7) (2026-09-06)


### Bug Fixes

* **inventory:** codex account list must carry a models array before it counts ([#27](https://github.com/batuta-ai/core/issues/27)) ([c6d2df6](https://github.com/batuta-ai/core/commit/c6d2df6a734cb814e5c98f3b04d1fbc382cf62a9))

## [1.1.0-beta.6](https://github.com/batuta-ai/core/compare/v1.1.0-beta.5...v1.1.0-beta.6) (2026-09-05)


### Features

* **release:** ship prebuilt binaries with goreleaser ([#23](https://github.com/batuta-ai/core/issues/23)) ([38585c0](https://github.com/batuta-ai/core/commit/38585c08ca0106c775b9e01b979b6a5ddb98b516)), closes [#22](https://github.com/batuta-ai/core/issues/22)

## [1.1.0-beta.5](https://github.com/batuta-ai/core/compare/v1.1.0-beta.4...v1.1.0-beta.5) (2026-09-05)


### Bug Fixes

* **inventory:** read codex models from the account list, bundled only as fallback ([#21](https://github.com/batuta-ai/core/issues/21)) ([4fd69e7](https://github.com/batuta-ai/core/commit/4fd69e7ede3344c7c6768e1f2b24d2f300e3a6db)), closes [#20](https://github.com/batuta-ai/core/issues/20)

## [1.1.0-beta.4](https://github.com/batuta-ai/core/compare/v1.1.0-beta.3...v1.1.0-beta.4) (2026-09-05)


### Features

* **routing:** plan task source, routing-table generation and retry-then-escalate policy ([#16](https://github.com/batuta-ai/core/issues/16)) ([4df559c](https://github.com/batuta-ai/core/commit/4df559c86fa19e6d4bfd48e9a31ae1c2fb751cd2))

## [1.1.0-beta.3](https://github.com/batuta-ai/core/compare/v1.1.0-beta.2...v1.1.0-beta.3) (2026-09-05)


### Bug Fixes

* **inventory:** bind models for claude, agy and cursor-agent ([#15](https://github.com/batuta-ai/core/issues/15)) ([d0bf813](https://github.com/batuta-ai/core/commit/d0bf8133d2539803700d9cfcb0f9a840aadeab14)), closes [#11](https://github.com/batuta-ai/core/issues/11)

## [1.1.0-beta.2](https://github.com/batuta-ai/core/compare/v1.1.0-beta.1...v1.1.0-beta.2) (2026-09-05)


### Bug Fixes

* **cli:** doctor inspects git with its own context ([#9](https://github.com/batuta-ai/core/issues/9)) ([3c13300](https://github.com/batuta-ai/core/commit/3c133007ab5085885636e9538684cd6fa69d4c39))

## [1.1.0-beta.1](https://github.com/batuta-ai/core/compare/v1.1.0-beta...v1.1.0-beta.1) (2026-09-05)


### Features

* **cli:** add capabilities subcommand and git-aware doctor ([#7](https://github.com/batuta-ai/core/issues/7)) ([b9e3154](https://github.com/batuta-ai/core/commit/b9e31547bf51875ac8d2722c4099855588e51194))

## [1.1.0-beta](https://github.com/batuta-ai/core/compare/v1.0.1...v1.1.0-beta) (2026-09-04)


### Features

* **cmd:** batuta binary with version, inventory and doctor ([b42226b](https://github.com/batuta-ai/core/commit/b42226bec433812e30060711263b97a368d495c1))

## [1.0.1](https://github.com/batuta-ai/core/compare/v1.0.0...v1.0.1) (2026-09-04)


### Bug Fixes

* retract the accidental v1.0.0 release and document the beta line ([d4a848e](https://github.com/batuta-ai/core/commit/d4a848ea9ac98e17f2daabc23ec2c0ddcbbb6ef3))

## 1.0.0 (2026-09-04)


### Features

* add guarded delivery preflight ([0209bf2](https://github.com/batuta-ai/core/commit/0209bf275adbcca50e40ca2e0ccc03f059f8719d))
* add redacted executor inventory core ([59f0c0d](https://github.com/batuta-ai/core/commit/59f0c0d2f6306620c654eb123305340f2237d057))
* add safe publication process boundary ([bf501c9](https://github.com/batuta-ai/core/commit/bf501c975aa4bff72b553943d1f89ef2c0433740))
* bind exact executor runtime pairs ([0f486b0](https://github.com/batuta-ai/core/commit/0f486b0445a3ef0bad207522b31bb259f8c3b5d7))
* carry catalog model costs into routing ([973c2bd](https://github.com/batuta-ai/core/commit/973c2bdb88be83cd5a0632d857c52eb5d9f5090f))
* coordinate parallel delivery waves ([485a3e4](https://github.com/batuta-ai/core/commit/485a3e441e8bb804ca318346655a41fc22cbeb62))
* enrich Claude and Agy inventory ([9040eb2](https://github.com/batuta-ai/core/commit/9040eb26f52a73773646793d65bc442b9e33e2ce))
* extract the daemon-free packages of batuta-compozy as batuta-ai/core ([4eca7be](https://github.com/batuta-ai/core/commit/4eca7be4fa677f184233050ebf62b545a155b9c8))
* integrate task commits deterministically ([910c4c6](https://github.com/batuta-ai/core/commit/910c4c6a657e621991842d7e6fb650bb5b6430c0))
* inventory local executor capabilities ([048358d](https://github.com/batuta-ai/core/commit/048358d2c6369ced46a61fb728916284870da389))
* normalize live executor inventory ([0e554f0](https://github.com/batuta-ai/core/commit/0e554f0fa1992825a7bb1b0f082e9a03cd63ad30))
* persist owned routing generations ([cd9cf42](https://github.com/batuta-ai/core/commit/cd9cf42a3f4097f27c883b99ba2f77df446dc619))
* persist parallel delivery graphs ([2c0e709](https://github.com/batuta-ai/core/commit/2c0e709411950bdf7e7063a3b2a21727a76aa3eb))
* pin migration-free delivery state ([1481c0a](https://github.com/batuta-ai/core/commit/1481c0a9667889b0de5a02a5fe8a2def5d2d7eff))
* plan trusted worktree publication ([5ee79fb](https://github.com/batuta-ai/core/commit/5ee79fbfc6443004feee1fe4db6c040114914010))
* publish worktree through bounded state machine ([555a14b](https://github.com/batuta-ai/core/commit/555a14b4180a2678d1b016f1c83f7dfdaaf41bde))
* reconcile delivery fallbacks across runs ([7e831a5](https://github.com/batuta-ai/core/commit/7e831a568eacceb13631b842632bdf68643182df))
* run dependency-safe task waves ([ff3191a](https://github.com/batuta-ai/core/commit/ff3191a27cf758da9d03fee25b4883ee93ebd65f))
* select domain complexity lanes ([ecfba7f](https://github.com/batuta-ai/core/commit/ecfba7f8bdf25fdaedf3cec58d5f7eaebbd95c1c))
* validate task lane classification ([2445295](https://github.com/batuta-ai/core/commit/2445295f5f53647ae01ff0bb63916ea9bcccfe1c))
* verify publication independently ([22948bd](https://github.com/batuta-ai/core/commit/22948bd471ef3e2bc5fbf0cc9dcf39d73acc2941))


### Bug Fixes

* accept earlier delivery ceilings and report typed tool errors ([6f527b4](https://github.com/batuta-ai/core/commit/6f527b4d13c88b90bc1da6dcf8c7708fccf7d589))
* align routing with live models ([0ba3f3c](https://github.com/batuta-ai/core/commit/0ba3f3cdce527b9f9197bb046a56b0f61cbe63ec))
* apply the routing matrix on a reused worktree that carries integrated tasks ([06aafe2](https://github.com/batuta-ai/core/commit/06aafe29e2c12aa0cd5eb45adb5e3674d100f351))
* close a deterministically blocked publication as a blocked delivery ([6a8c308](https://github.com/batuta-ai/core/commit/6a8c308501bd1a4485ebcef6274c2804d51ade61))
* close delivery mutation boundaries ([a683f21](https://github.com/batuta-ai/core/commit/a683f2165bab8d01db19a3af87fe46a702bbe17f))
* close parallel delivery release gates ([2bc4201](https://github.com/batuta-ai/core/commit/2bc4201e51b6a3d273d45da337414873d2833f81))
* harden delivery retries and recovery ([99a6c8b](https://github.com/batuta-ai/core/commit/99a6c8bb1afab4e490b5807de8c982ae596b42ac))
* ignore build artifacts when collecting task tracking evidence ([353ad69](https://github.com/batuta-ai/core/commit/353ad69ebd15d5f1574e97347d4085309a565664))
* make routing catalog-driven ([3028bf3](https://github.com/batuta-ai/core/commit/3028bf3f63ce875e32cba6f7c643e2d1d543d9b8))
* preserve provider routing evidence ([ac83cab](https://github.com/batuta-ai/core/commit/ac83cab0b60840a499bb38800cb8e94f017216cf))
* preserve routing alignment generations ([ac727ed](https://github.com/batuta-ai/core/commit/ac727ed6ea081fbf97be13d159be3bf519ae8b24))
* prove a blocked delivery by its recorded publication blockers ([822afd6](https://github.com/batuta-ai/core/commit/822afd6f542f3851421c67d43052bdb603d12d09))
* **publication:** refresh worktree status ([cfc7f99](https://github.com/batuta-ai/core/commit/cfc7f99d41ff2cda7f4f5928efcf2d22480aadfe))
* raise the delivery token ceiling to 500M ([a79d1c5](https://github.com/batuta-ai/core/commit/a79d1c5cd5ace32a6df195bfba9335bc38359abd))
* reject truncated Git evidence ([5a1d342](https://github.com/batuta-ai/core/commit/5a1d3426da8af0e55d971e5e6ca029142eda935a))
* request JSON output from compozy version probe ([baa5ba5](https://github.com/batuta-ai/core/commit/baa5ba5c52d81a7310c2e0ebee548d964a62a202))
* size the delivery budget for CompozyOS accounting and start runs as the agent ([7cb8647](https://github.com/batuta-ai/core/commit/7cb864740441834b790db3a15017933a064ec5ed))
* start a publication-only attempt when every task is already integrated ([cc5a692](https://github.com/batuta-ai/core/commit/cc5a69233d2a7b796b96b6a4f380830de4f41215))
* treat a repository without a remote as a local-only publication ([f34f485](https://github.com/batuta-ai/core/commit/f34f485c2f574e3c987a49c661d1a4a6f2bf2891))

## Changelog

Releases are generated by release-please from commit messages.

## Retracted versions

- `v1.0.0` and `v1.0.1` were published before the beta line and are retracted in `go.mod`. Use the current `v1.1.0-beta.N`.
