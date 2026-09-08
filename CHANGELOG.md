# Changelog

## [1.1.0-beta.21](https://github.com/batuta-ai/core/compare/v1.1.0-beta.20...v1.1.0-beta.21) (2026-09-08)


### Features

* batuta review runs proof-backed criteria in the reviewed tree and se ([043124a](https://github.com/batuta-ai/core/commit/043124adb5d144e9092083b37a4fb08161ce6253))
* **review:** proof-backed criteria run in the reviewed tree; commit subjects keep their case ([ddb4459](https://github.com/batuta-ai/core/commit/ddb445916d169d7de9918e4572cc5edf7755208c))
* the integration commit subject keeps the title's case and cuts at a ([f5b5f31](https://github.com/batuta-ai/core/commit/f5b5f3150ac1d3ad6e2f03ba6d74a15b92fc041a))


### Bug Fixes

* **loop:** a space exactly at byte 68 is a cut boundary for the commit subject ([96c5950](https://github.com/batuta-ai/core/commit/96c59506b58111a219fd2cdd34eaaf252520f22a))

## [1.1.0-beta.20](https://github.com/batuta-ai/core/compare/v1.1.0-beta.19...v1.1.0-beta.20) (2026-09-08)


### Features

* --abandon, --answer and --resume refuse a delivery whose runner is a ([8842df4](https://github.com/batuta-ai/core/commit/8842df4d0d54ee1063d63e79f9db723d6408eaab))
* a continuation is judged against the attempt's base, never against t ([8ea4049](https://github.com/batuta-ai/core/commit/8ea4049d79aab8ffdd2cdb8c1710785d96abfc5a))
* a question on the last allowed execution blocks the task with the qu ([61fc763](https://github.com/batuta-ai/core/commit/61fc76379ee3a64945c6a2842820acaa757f295f))
* a usage limit that outlasts the wait budget falls back to the next r ([aced9f7](https://github.com/batuta-ai/core/commit/aced9f7c1256189f7b631c1c9797dde73c4065d9))
* an already-satisfied task does not end the delivery; dependents run ([1009933](https://github.com/batuta-ai/core/commit/1009933f0d35d614ae9ad3da086077ec84357635))
* batuta review: stable incremental state, pending cohorts carried ove ([de384b4](https://github.com/batuta-ai/core/commit/de384b45e0176bae9d7b15ee398393ae4c1a588d))
* batuta review: the subcommand, its artefacts and the printed report ([1d77034](https://github.com/batuta-ai/core/commit/1d77034655bc793d3b3417ed5fefb7e53ee4ac0a))
* cleanParked walks history with bounded output ([c8dcab7](https://github.com/batuta-ai/core/commit/c8dcab7ad13be03ed93ca7c98c5baa0766581b9e))
* enter submits the answer; ctrl+j and shift+enter insert a newline; t ([8ebe72a](https://github.com/batuta-ai/core/commit/8ebe72a5e688de39c7092eaf5d08f8134fc68371))
* escalation to self blocks the task in RecordFailure instead of plann ([281a184](https://github.com/batuta-ai/core/commit/281a18445570941a8a2b9233b693d6088076052d))
* graph transitions persist through the ownership store: limit fallbac ([3676943](https://github.com/batuta-ai/core/commit/3676943832b7ac8752a55972d66670e25b833171))
* inventory never starts an agent turn: cursor, opencode and codex ada ([afcf4d1](https://github.com/batuta-ai/core/commit/afcf4d11ea8cc0cc20dd1610c8d3d70cbe9ff946))
* loop: bounded reset-time parsing, empty-journal takeover, staged wor ([950cdce](https://github.com/batuta-ai/core/commit/950cdceaa264e235560a7d98b64e9cb2d16ee85f))
* **loop:** hardening — dead ends, limit fallback, cursor probes, batuta review, presence lock ([3e64e13](https://github.com/batuta-ai/core/commit/3e64e13558afb27bcd6304198064d0e026eae00f))
* overlay text is sanitised; the picker honours glyphs and language an ([6cdc6bb](https://github.com/batuta-ai/core/commit/6cdc6bb6d53084722e2ff18facc7aa2dd2a9c71b))
* parking preserves committed executor work: a clean worktree whose HE ([69d457b](https://github.com/batuta-ai/core/commit/69d457b2401198c4471444c23aaa39d4cf1aaea6))
* poll results carry their identity; the picker never starves the back ([5b87e41](https://github.com/batuta-ai/core/commit/5b87e4145bca1f36731232f5bb9222dbbb16bf7a))
* resume, answer and abandon acquire exclusive delivery ownership befo ([0b41238](https://github.com/batuta-ai/core/commit/0b41238a8268949a2692629d4b9c8e064b8d9bf8))
* review honesty: truncated lint output is an error, the state key is ([7f27bb0](https://github.com/batuta-ai/core/commit/7f27bb0db59b32ca778c1d9640c8ef6a38730c4b))
* review package robustness: submodule gitlinks, escaped filenames, st ([5a849ec](https://github.com/batuta-ai/core/commit/5a849ec5e63f99f884e7e1ef17a32ee755202313))
* review: patches by file identity, no publication beneath gitlinks, c ([6cc933d](https://github.com/batuta-ai/core/commit/6cc933d6c0b40fba17b729e2a3dbe878cee722ed))
* reviewer sessions: one read-only executor per cohort, in parallel, t ([e802c99](https://github.com/batuta-ai/core/commit/e802c99ee134cc0ca74eb7011ce9784fa94d8ec5))
* spec conformance and linter overlap ([735219d](https://github.com/batuta-ai/core/commit/735219ded1c6e437cad1ed2812eac5a47e04df73))
* the answer binds to the shown delivery and question and refuses whil ([52d4bfd](https://github.com/batuta-ai/core/commit/52d4bfd5c7574351986a8866e83486a732f60f45))
* the ownership lock is crash-safe, takeover is serialised across proc ([dfcce25](https://github.com/batuta-ai/core/commit/dfcce257ebce33f314bda4a1a36bcc963426529d))
* the presence lock is owned and symlink-safe; the pager is cancellabl ([0251bc9](https://github.com/batuta-ai/core/commit/0251bc9a06622c654f5c8e057ba62f41f38cdc10))
* the review package: manifest, cohorts, findings schema, merge and ve ([8057f16](https://github.com/batuta-ai/core/commit/8057f16fa6b279177c1d0fa8608a1c27333c85da))
* uncommitted executor work is snapshotted before a park, a retry or a ([f394044](https://github.com/batuta-ai/core/commit/f394044945547a88ff78f3d5040cab016108ad11))


### Bug Fixes

* **loop:** --answer ages a malformed lock and binds ownership to the answerable delivery ([e0fc087](https://github.com/batuta-ai/core/commit/e0fc08728668eadbe92c345d788e8d5a4ec4dcb5))
* **loop:** park a conflicted index; finalisation survives bookkeeping failures ([10092c8](https://github.com/batuta-ai/core/commit/10092c816c45c9b88e69897685eccbcdcc20526a))
* **loop:** Run waits for in-flight attempts; output writes are serialised ([a940181](https://github.com/batuta-ai/core/commit/a9401817c0333cb943a2eeebf38573e7f52ebd39))
* **loop:** terminal record precedes parked-ref deletion; scoped bookkeeping recovery; bounded conflict scan ([78edbcc](https://github.com/batuta-ai/core/commit/78edbccbe1b7329c634050779790d25909050b9a))
* **loop:** the terminal record stays last; deletions retried on recovery; --answer refuses a running task ([c3f49d8](https://github.com/batuta-ai/core/commit/c3f49d85e6fdec7bf6e06b9c6d72f0e070e58486))
* **review:** an oversized file becomes its own cohort instead of an error ([84a7e61](https://github.com/batuta-ai/core/commit/84a7e6174d31b5e073430aaa31502869b50280e8))

## [1.1.0-beta.19](https://github.com/batuta-ai/core/compare/v1.1.0-beta.18...v1.1.0-beta.19) (2026-09-08)


### Features

* a paint layer in the renderer: segments, line kinds, one SGR table; ([2c043ad](https://github.com/batuta-ai/core/commit/2c043add7e708f876b1ce532671bbc74cb961715))
* animation: spinner on running work, eased progress bars ([02f8ccc](https://github.com/batuta-ai/core/commit/02f8ccc499a12cde51524a56b044ab50bd207867))
* bars, header, boxes, detail, keys and log painted ([24a971b](https://github.com/batuta-ai/core/commit/24a971b4cbec8fe25f67c6804f3654a4d23627df))
* log panel scrolling and mouse wheel ([c321435](https://github.com/batuta-ai/core/commit/c321435b5c63ea1601cccefa077726f4b5ee9daa))
* loop.Watch runs the program; the hand-rolled terminal code goes ([bfb6d45](https://github.com/batuta-ai/core/commit/bfb6d45b3847bacbc57362945a7c2fa82480081f))
* **loop:** batuta watch on Bubble Tea v2 — event-driven, styled, answer in place, delivery picker ([4dde022](https://github.com/batuta-ai/core/commit/4dde022238b6ae0cadf0aa4bab25d59924c9d20b))
* submitting an answer resumes the loop detached; the watch keeps foll ([d3e9a4a](https://github.com/batuta-ai/core/commit/d3e9a4a4c13d79eda70b9e207efb320e2611bb32))
* the answer editor: r opens a textarea overlay, ctrl+enter, alt+enter ([7b4fbbe](https://github.com/batuta-ai/core/commit/7b4fbbeec11e39e0a1ee2f7d05eee704443c438a))
* the delivery picker: d opens a list of deliveries; the watch without ([be0df8e](https://github.com/batuta-ai/core/commit/be0df8ee5ff4a3a40cce52a2365e610e3692d748))
* the program redraws only on change: journal poller, log tail, clock ([83d4548](https://github.com/batuta-ai/core/commit/83d45487e8a05536b7bf1f83eab00db9737942e1))
* the watch is a Bubble Tea model over PanelModel and Render ([35ec592](https://github.com/batuta-ai/core/commit/35ec592ad164f8ae63fbc87c41e9333cfe518996))
* the watch never quits by itself and shows loop presence from a lock ([2c97f77](https://github.com/batuta-ai/core/commit/2c97f7735a8fc570976e2e01a5e16fdf426b6282))


### Bug Fixes

* **loop:** watch frames never wrap or overflow the terminal ([3e24c94](https://github.com/batuta-ai/core/commit/3e24c94dbe71a746b2b2ad4e07d6f8632d34a519))

## [1.1.0-beta.18](https://github.com/batuta-ai/core/compare/v1.1.0-beta.17...v1.1.0-beta.18) (2026-09-07)


### Features

* a view model summarises the journal for the dashboard ([3cc1040](https://github.com/batuta-ai/core/commit/3cc1040691ace646eb3c9c9742f2acef27856f6b))
* batuta watch opens the live dashboard by default ([3483622](https://github.com/batuta-ai/core/commit/3483622988edac78c547ef02ad2c0df97a525454))
* executor output streams to the run log while the session runs ([e78879b](https://github.com/batuta-ai/core/commit/e78879bf23d240bcb4fc9da17af0ea21faf6b0e5))
* keyboard navigation with a no-TTY fallback ([0901aeb](https://github.com/batuta-ai/core/commit/0901aebcba5a7f9b318993791867a83edbd5b809))
* **loop:** dashboard v2 — boxed panels, waves, gates, live log, keyboard; batuta watch ([a3b75ae](https://github.com/batuta-ai/core/commit/a3b75ae7e02eedcb73bba159350f92eba78ae322))
* the logs panel tails the active execution's run log ([83a470b](https://github.com/batuta-ai/core/commit/83a470bef9fb086f371c4e4c37bc87341a4324e6))
* the renderer draws boxed panels, bars and the wave table at the term ([f7401a8](https://github.com/batuta-ai/core/commit/f7401a8faa7d11f3cda5a45f695a54416631da29))


### Bug Fixes

* **loop:** dashboard reads the real run log; watch --once renders the panel ([30858b3](https://github.com/batuta-ai/core/commit/30858b3866627273b004f1e97bb4acb80301cd62)), closes [#64](https://github.com/batuta-ai/core/issues/64)

## [1.1.0-beta.17](https://github.com/batuta-ai/core/compare/v1.1.0-beta.16...v1.1.0-beta.17) (2026-09-06)


### Features

* archiving a plan ticks its phase in the roadmap ([5cbec72](https://github.com/batuta-ai/core/commit/5cbec726e586382e53f7d33505a81ff45c0cd6b4))
* batuta loop --roadmap runs the phases in order, one delivery per app ([700db08](https://github.com/batuta-ai/core/commit/700db08783decaf782fdfb7a64abe7ba1641ae15))
* **loop:** roadmap — phases above plans, batuta loop --roadmap ([8715453](https://github.com/batuta-ai/core/commit/8715453bb104ac03d252aba1a139bfdd2a83d2cd))
* parse `.batuta/roadmap.md` into phases with an optional plan slug ([6461d5c](https://github.com/batuta-ai/core/commit/6461d5c04f8096f82dd460ed1a15b1fe29466e40))
* the opened record carries roadmap and phase ([2258a99](https://github.com/batuta-ai/core/commit/2258a99294654e4e6adbc5e44e9b27b5c51917d0))

## [1.1.0-beta.16](https://github.com/batuta-ai/core/compare/v1.1.0-beta.15...v1.1.0-beta.16) (2026-09-06)


### Bug Fixes

* **routing:** a conflicting candidate re-executes on the same runtime ([88e5f93](https://github.com/batuta-ai/core/commit/88e5f9390efce01974c5161b87078a578993ca93))
* **routing:** a conflicting candidate re-executes on the same runtime ([b90a737](https://github.com/batuta-ai/core/commit/b90a737bfa16824300a51638a5b19fd0c869b224)), closes [#18](https://github.com/batuta-ai/core/issues/18)

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
