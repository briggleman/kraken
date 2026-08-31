# Changelog

## [0.34.1](https://github.com/briggleman/kraken/compare/v0.34.0...v0.34.1) (2026-08-31)


### Bug Fixes

* **web:** reset the agent chip's phase when its drift episode ends ([#176](https://github.com/briggleman/kraken/issues/176)) ([1388fdb](https://github.com/briggleman/kraken/commit/1388fdb3498cd348e9981b59a471102b716bd40c)), closes [#159](https://github.com/briggleman/kraken/issues/159)

## [0.34.0](https://github.com/briggleman/kraken/compare/v0.33.0...v0.34.0) (2026-08-31)


### Features

* **web:** the agent chip gets its five states ([#174](https://github.com/briggleman/kraken/issues/174)) ([bb610be](https://github.com/briggleman/kraken/commit/bb610bec79143653792a50ea1df37efd3b6fb11c)), closes [#159](https://github.com/briggleman/kraken/issues/159)

## [0.33.0](https://github.com/briggleman/kraken/compare/v0.32.0...v0.33.0) (2026-08-31)


### ⚠ BREAKING CHANGES

* **panel:** POST /nodes/{id}/agent-update returns 202 with a job body ({job_id, phase, bytes_sent, bytes_total}) instead of 200 with {from_version, to_version, restarting}. POST /nodes/agent-update-all is removed.

### Features

* **panel:** agent-update answers 202 and streams in the background ([#172](https://github.com/briggleman/kraken/issues/172)) ([d6a1e5a](https://github.com/briggleman/kraken/commit/d6a1e5ab526d78c95bb6831a68ee6eb4d446c5a5)), closes [#163](https://github.com/briggleman/kraken/issues/163)

## [0.32.0](https://github.com/briggleman/kraken/compare/v0.31.0...v0.32.0) (2026-08-31)


### Features

* **web:** lock a node to hold new placements ([#171](https://github.com/briggleman/kraken/issues/171)) ([49b8132](https://github.com/briggleman/kraken/commit/49b81329a1d712d2cf93f7d977b31e37194804da)), closes [#167](https://github.com/briggleman/kraken/issues/167)


### Bug Fixes

* **panel:** refuse to delete a node that still owns servers ([#169](https://github.com/briggleman/kraken/issues/169)) ([a6db82b](https://github.com/briggleman/kraken/commit/a6db82ba4dc28850e3c3aa8701b4df9e3d7930ad)), closes [#166](https://github.com/briggleman/kraken/issues/166)

## [0.31.0](https://github.com/briggleman/kraken/compare/v0.30.2...v0.31.0) (2026-08-31)


### Features

* **web:** delete a node, and re-mint an enrollment token ([#165](https://github.com/briggleman/kraken/issues/165)) ([61c5a40](https://github.com/briggleman/kraken/commit/61c5a4007d6a46f10f503bbe859849cd8d1da272))

## [0.30.2](https://github.com/briggleman/kraken/compare/v0.30.1...v0.30.2) (2026-08-31)


### Bug Fixes

* **deploy:** let the agent replace its own binary ([#162](https://github.com/briggleman/kraken/issues/162)) ([6119ade](https://github.com/briggleman/kraken/commit/6119ade5694f434bb24585dbc598841194cf4dce)), closes [#114](https://github.com/briggleman/kraken/issues/114)

## [0.30.1](https://github.com/briggleman/kraken/compare/v0.30.0...v0.30.1) (2026-08-31)


### Bug Fixes

* **panel:** report the agent's refusal, not "stream binary: EOF" ([#160](https://github.com/briggleman/kraken/issues/160)) ([0169b09](https://github.com/briggleman/kraken/commit/0169b098a7d09c0a6987ddaf2ff09c85635feb24)), closes [#114](https://github.com/briggleman/kraken/issues/114)

## [0.30.0](https://github.com/briggleman/kraken/compare/v0.29.0...v0.30.0) (2026-08-31)


### Features

* **web:** flag nodes whose agent the panel has outrun ([#156](https://github.com/briggleman/kraken/issues/156)) ([3c51892](https://github.com/briggleman/kraken/commit/3c518928f10d67e6db6942561e3bf8b7cb220a80))

## [0.29.0](https://github.com/briggleman/kraken/compare/v0.28.0...v0.29.0) (2026-08-28)


### Features

* **web:** animate the events floor as a continuous ticker ([#152](https://github.com/briggleman/kraken/issues/152)) ([53450e3](https://github.com/briggleman/kraken/commit/53450e345f9c86fcd80f1fcd1af3415ddba46ef9))
* **web:** sftp connection details from the station strip ([#155](https://github.com/briggleman/kraken/issues/155)) ([cd9bf0f](https://github.com/briggleman/kraken/commit/cd9bf0f9d7f11cebb23da04b9d7a052616c718ee))

## [0.28.0](https://github.com/briggleman/kraken/compare/v0.27.0...v0.28.0) (2026-08-28)


### Features

* rename a node from node settings ([#148](https://github.com/briggleman/kraken/issues/148)) ([5179f0c](https://github.com/briggleman/kraken/commit/5179f0c00e4969b4d76e1b8ddcb8fed375ce9dd4))


### Bug Fixes

* honor memory_mb when creating a server ([#149](https://github.com/briggleman/kraken/issues/149)) ([739b886](https://github.com/briggleman/kraken/commit/739b886d547a628207ee1263a1f35df26a0f6bb2))

## [0.27.0](https://github.com/briggleman/kraken/compare/v0.26.1...v0.27.0) (2026-08-28)


### Features

* report real node host vitals on the node bands ([#142](https://github.com/briggleman/kraken/issues/142)) ([995e41f](https://github.com/briggleman/kraken/commit/995e41f86ab7a3f8a019276ba35337b3e9c285ea)), closes [#128](https://github.com/briggleman/kraken/issues/128)

## [0.26.1](https://github.com/briggleman/kraken/compare/v0.26.0...v0.26.1) (2026-08-27)


### Bug Fixes

* **agent:** reject peer-agent certs on the gRPC listener ([#138](https://github.com/briggleman/kraken/issues/138)) ([6ddd8be](https://github.com/briggleman/kraken/commit/6ddd8bed0a2aa9b62c583f5ad8de3d9f76e215b5))

## [0.26.0](https://github.com/briggleman/kraken/compare/v0.25.1...v0.26.0) (2026-08-27)


### Features

* **web:** port the panel UI to Svelte 5, generated from the design mock ([#134](https://github.com/briggleman/kraken/issues/134)) ([135e123](https://github.com/briggleman/kraken/commit/135e123c2e4615daeca61bd027bfc24412f0b101))

## [0.25.1](https://github.com/briggleman/kraken/compare/v0.25.0...v0.25.1) (2026-08-27)


### Bug Fixes

* **images:** add xz-utils to steam-base so Factorio can install ([30a5928](https://github.com/briggleman/kraken/commit/30a5928ee8c84c522f005e106b997f44b7fd3a92)), closes [#132](https://github.com/briggleman/kraken/issues/132)

## [0.25.0](https://github.com/briggleman/kraken/compare/v0.24.0...v0.25.0) (2026-08-20)


### Features

* **specs:** enable Enshrouded on Linux nodes via Wine ([#120](https://github.com/briggleman/kraken/issues/120)) ([c73d81c](https://github.com/briggleman/kraken/commit/c73d81c06a9b7d671155ffaa2e32afc5b47af251))


### Bug Fixes

* **deploy:** embed all agent platforms in the panel image ([#123](https://github.com/briggleman/kraken/issues/123)) ([5c3a77c](https://github.com/briggleman/kraken/commit/5c3a77cbe8958c4444416ae3ab903a702f26a184))
* **panel:** drop the racy first-contact reconcile on node registration ([#121](https://github.com/briggleman/kraken/issues/121)) ([eefe53c](https://github.com/briggleman/kraken/commit/eefe53c479be1aa51e365360360f9327e516e4ef))

## [0.24.0](https://github.com/briggleman/kraken/compare/v0.23.1...v0.24.0) (2026-08-20)


### Features

* cordon nodes, fleet agent updates, tunnel SFTP proxy, and skew/registration/stream fixes ([#118](https://github.com/briggleman/kraken/issues/118)) ([d5c9243](https://github.com/briggleman/kraken/commit/d5c9243b09873882fb37d1ecbc4b87d5338c161e))

## [0.23.1](https://github.com/briggleman/kraken/compare/v0.23.0...v0.23.1) (2026-08-20)


### Bug Fixes

* **agent:** make SCM recovery restart the service on error exits ([#114](https://github.com/briggleman/kraken/issues/114)) ([36c7b5f](https://github.com/briggleman/kraken/commit/36c7b5f5d87db955c269c9a23c01060f6a805f7a))
* **web:** register tunnel nodes with the OS chosen in the add-node dialog ([#117](https://github.com/briggleman/kraken/issues/117)) ([27975c2](https://github.com/briggleman/kraken/commit/27975c26690321a8b357bd39929c0a566e6e5605))
* **web:** resolve the add-node flow when the node lands partial ([#116](https://github.com/briggleman/kraken/issues/116)) ([53fa99d](https://github.com/briggleman/kraken/commit/53fa99d96d03647016dbae01d3f3fe4907fb655a))

## [0.23.0](https://github.com/briggleman/kraken/compare/v0.22.2...v0.23.0) (2026-08-20)


### Features

* **web:** confirm node onboarding success with a clear done state ([#113](https://github.com/briggleman/kraken/issues/113)) ([04057e0](https://github.com/briggleman/kraken/commit/04057e0e5e220e934d102e23ab43e8b66856bcc1))
* **web:** default new node enrollment to tunnel mode ([#110](https://github.com/briggleman/kraken/issues/110)) ([0ca2e71](https://github.com/briggleman/kraken/commit/0ca2e71d49fa5c6baa950b8b5383fa9dc774d6b9))

## [0.22.2](https://github.com/briggleman/kraken/compare/v0.22.1...v0.22.2) (2026-08-20)


### Bug Fixes

* **web:** drop navbar duplicates from the user menu ([#108](https://github.com/briggleman/kraken/issues/108)) ([5b87710](https://github.com/briggleman/kraken/commit/5b8771083212280ef1f72d2be268ad86ca1cbf20))

## [0.22.1](https://github.com/briggleman/kraken/compare/v0.22.0...v0.22.1) (2026-08-19)


### Bug Fixes

* surface install failures and make placement pinning real ([#103](https://github.com/briggleman/kraken/issues/103)) ([0c6f8b6](https://github.com/briggleman/kraken/commit/0c6f8b651b7daa4e9c2ba36eaa167088f76603fc))

## [0.22.0](https://github.com/briggleman/kraken/compare/v0.21.0...v0.22.0) (2026-08-19)


### Features

* flip an existing node between direct and tunnel modes ([#101](https://github.com/briggleman/kraken/issues/101)) ([46c7ccf](https://github.com/briggleman/kraken/commit/46c7ccf4f881a149c0fdf7f79cf6befdc4117810))

## [0.21.0](https://github.com/briggleman/kraken/compare/v0.20.0...v0.21.0) (2026-08-19)


### Features

* reverse-connection tunnel mode - nodes need zero inbound ports ([#85](https://github.com/briggleman/kraken/issues/85)) ([4d43629](https://github.com/briggleman/kraken/commit/4d436299065d7257461ae2f309a60743d202d69b))

## [0.20.0](https://github.com/briggleman/kraken/compare/v0.19.0...v0.20.0) (2026-08-19)


### Features

* **web:** fleet header nav with live status cluster and instrument surfaces ([#82](https://github.com/briggleman/kraken/issues/82)) ([0261329](https://github.com/briggleman/kraken/commit/026132901f42d9154baa509ba36dad9b9d438b21))

## [0.19.0](https://github.com/briggleman/kraken/compare/v0.18.0...v0.19.0) (2026-08-18)


### Features

* panel-pushed agent self-update with automatic rollback ([#80](https://github.com/briggleman/kraken/issues/80)) ([aa0bb2e](https://github.com/briggleman/kraken/commit/aa0bb2ee032a30a99ad46af4c13a13eb6f2129c7))

## [0.18.0](https://github.com/briggleman/kraken/compare/v0.17.0...v0.18.0) (2026-08-18)


### Features

* one-command Windows agent install ([#77](https://github.com/briggleman/kraken/issues/77)) ([acfa86d](https://github.com/briggleman/kraken/commit/acfa86d613da367cdff8113c94ea683be38e0d42))

## [0.17.0](https://github.com/briggleman/kraken/compare/v0.16.0...v0.17.0) (2026-08-18)


### Features

* **agent:** run as a native Windows service ([#76](https://github.com/briggleman/kraken/issues/76)) ([a15127f](https://github.com/briggleman/kraken/commit/a15127fb75526ccdf15b9e7a075cea368dd5491b))

## [0.16.0](https://github.com/briggleman/kraken/compare/v0.15.2...v0.16.0) (2026-08-18)


### Features

* single-command remote agent enrollment ([#74](https://github.com/briggleman/kraken/issues/74)) ([c1f69c0](https://github.com/briggleman/kraken/commit/c1f69c069135ba7aa9296e7fc7783694a48afbe0))

## [0.15.2](https://github.com/briggleman/kraken/compare/v0.15.1...v0.15.2) (2026-08-11)


### Bug Fixes

* **deps:** bump grpc-go to 1.82.1 and x/text to 0.39.0 ([#71](https://github.com/briggleman/kraken/issues/71)) ([7750c3f](https://github.com/briggleman/kraken/commit/7750c3fa0e1174b539bb79d9eb0918f35c7ae329))

## [0.15.1](https://github.com/briggleman/kraken/compare/v0.15.0...v0.15.1) (2026-08-11)


### Bug Fixes

* **web:** bump react-router to 7.18.2 and clear npm advisories ([#70](https://github.com/briggleman/kraken/issues/70)) ([543c222](https://github.com/briggleman/kraken/commit/543c2229c7cb3eea37b8b4cbe22faddd43e1d7e9))

## [0.15.0](https://github.com/briggleman/kraken/compare/v0.14.2...v0.15.0) (2026-08-03)


### Features

* **panel:** content security policy and hardened response headers ([#67](https://github.com/briggleman/kraken/issues/67)) ([ba8bb35](https://github.com/briggleman/kraken/commit/ba8bb355e72ddc6ef20a2ac299376cf1dc201550))
* **ui:** self-host the brand faces instead of fetching Google Fonts ([#68](https://github.com/briggleman/kraken/issues/68)) ([2c11530](https://github.com/briggleman/kraken/commit/2c11530c7cabeb6591a3360b18bd0cd126834e92))

## [0.14.2](https://github.com/briggleman/kraken/compare/v0.14.1...v0.14.2) (2026-08-03)


### Bug Fixes

* **ui:** show a crashed server's last output instead of a dead end ([#65](https://github.com/briggleman/kraken/issues/65)) ([a20ac1b](https://github.com/briggleman/kraken/commit/a20ac1b308aaa730572cf0705654a5ec2709184b))

## [0.14.1](https://github.com/briggleman/kraken/compare/v0.14.0...v0.14.1) (2026-08-03)


### Bug Fixes

* **ui:** stop the fleet dashboard reporting state it does not have ([#63](https://github.com/briggleman/kraken/issues/63)) ([897e3e7](https://github.com/briggleman/kraken/commit/897e3e7f4aac76ae5dd6ee06935c923664ab5aaa))

## [0.14.0](https://github.com/briggleman/kraken/compare/v0.13.0...v0.14.0) (2026-07-31)


### Features

* **ui:** show agent version on /nodes and a panel version stamp ([#57](https://github.com/briggleman/kraken/issues/57)) ([5b0a91d](https://github.com/briggleman/kraken/commit/5b0a91d46dc77c6585cb3d373966ba5d170b2e0a))

## [0.13.0](https://github.com/briggleman/kraken/compare/v0.12.0...v0.13.0) (2026-07-31)


### Features

* **nodes:** tri-state health (online/partial/offline) + watchdog re-adoption ([#52](https://github.com/briggleman/kraken/issues/52)) ([#55](https://github.com/briggleman/kraken/issues/55)) ([f0c2883](https://github.com/briggleman/kraken/commit/f0c288303d6d6b5d18da0d14c8f6ec74ebe237b4))

## [0.12.0](https://github.com/briggleman/kraken/compare/v0.11.1...v0.12.0) (2026-07-31)


### Features

* **agent:** config file, flags, and a --root layout ([#49](https://github.com/briggleman/kraken/issues/49)) ([#51](https://github.com/briggleman/kraken/issues/51)) ([06b9365](https://github.com/briggleman/kraken/commit/06b936555e2c935808a6025c9d21f50fc0237611))

## [0.11.1](https://github.com/briggleman/kraken/compare/v0.11.0...v0.11.1) (2026-07-30)


### Bug Fixes

* **agent:** resolve bind-mount sources against the host, not the Agent ([#48](https://github.com/briggleman/kraken/issues/48)) ([a51c80d](https://github.com/briggleman/kraken/commit/a51c80db2bf4087b9dc0ab36733f063ccb39f6ba))

## [0.11.0](https://github.com/briggleman/kraken/compare/v0.10.0...v0.11.0) (2026-07-30)


### Features

* specs list view with hero art and neutral platform marks ([#45](https://github.com/briggleman/kraken/issues/45)) ([55cf798](https://github.com/briggleman/kraken/commit/55cf798e666b7272dde8a0a0922514bfa4d14b29))

## [0.10.0](https://github.com/briggleman/kraken/compare/v0.9.0...v0.10.0) (2026-07-10)


### Features

* uniform create wizard, unified settings tab with hot_reload-aware messaging ([#42](https://github.com/briggleman/kraken/issues/42)) ([6b8d967](https://github.com/briggleman/kraken/commit/6b8d96734c3826fa5009df1616ad7d6ecb7cd155))

## [0.9.0](https://github.com/briggleman/kraken/compare/v0.8.3...v0.9.0) (2026-07-10)


### Features

* node capacity editing, eligible-node placement filter, default port ranges, per-node scheduler errors ([#40](https://github.com/briggleman/kraken/issues/40)) ([9ad7125](https://github.com/briggleman/kraken/commit/9ad7125ff7603df6ec05a4ac2a377bf8a34bdd8c))

## [0.8.3](https://github.com/briggleman/kraken/compare/v0.8.2...v0.8.3) (2026-07-09)


### Bug Fixes

* **enroll:** agent reports its port; registration prefill is IP-first ([#37](https://github.com/briggleman/kraken/issues/37)) ([6d2c429](https://github.com/briggleman/kraken/commit/6d2c42925111d619801fc580fe8fb424174acb71))

## [0.8.2](https://github.com/briggleman/kraken/compare/v0.8.1...v0.8.2) (2026-07-09)


### Bug Fixes

* **ui:** unified node-connect flow, FontAwesome platform icons, footer kraken ([#35](https://github.com/briggleman/kraken/issues/35)) ([ed55dca](https://github.com/briggleman/kraken/commit/ed55dcabcf0f37e332f3782f30730f81ad39a7bd))

## [0.8.1](https://github.com/briggleman/kraken/compare/v0.8.0...v0.8.1) (2026-07-09)


### Bug Fixes

* **setup:** latch onboarding completion + restrict /setup to internal networks ([#33](https://github.com/briggleman/kraken/issues/33)) ([c0541e2](https://github.com/briggleman/kraken/commit/c0541e2b602b900e649781be4365cc235ccfb42f))

## [0.8.0](https://github.com/briggleman/kraken/compare/v0.7.0...v0.8.0) (2026-07-09)


### Features

* **wine:** steam-wine image — Windows-only games on Linux nodes ([#30](https://github.com/briggleman/kraken/issues/30)) ([2c26cd8](https://github.com/briggleman/kraken/commit/2c26cd804ca4706a3290f76a5e67234489932987))

## [0.7.0](https://github.com/briggleman/kraken/compare/v0.6.0...v0.7.0) (2026-07-09)


### Features

* **mtls:** automatic agent cert rotation over the reconcile channel ([#28](https://github.com/briggleman/kraken/issues/28)) ([6e47746](https://github.com/briggleman/kraken/commit/6e477465092bbcfb835ebe5cab0dbe5ec9f07e69))

## [0.6.0](https://github.com/briggleman/kraken/compare/v0.5.1...v0.6.0) (2026-07-09)


### ⚠ BREAKING CHANGES

* **setup:** the gRPC package renamed cthulhu.agent.v1 → kraken.agent.v1, changing every Panel↔Agent RPC path. Upgrade the Panel and all Agents together; mixed versions fail with 'unknown method'. Certificates and persisted data are unaffected.

### Features

* **setup:** remote-node onboarding overhaul + mTLS debug logging ([2951867](https://github.com/briggleman/kraken/commit/295186751d058d6a9afc43d96f6304726921b4f1))

## [0.5.1](https://github.com/briggleman/kraken/compare/v0.5.0...v0.5.1) (2026-07-09)


### Bug Fixes

* **deploy:** panel-state volume must be nonroot-owned ([#23](https://github.com/briggleman/kraken/issues/23)) ([bcf372f](https://github.com/briggleman/kraken/commit/bcf372f95382e91c449a9ea58524b4b308ff7c01))
* **panel:** keep auto-signed client cert in memory, drop panel-init sidecar ([#25](https://github.com/briggleman/kraken/issues/25)) ([d0199b1](https://github.com/briggleman/kraken/commit/d0199b1aa9d1f3e85a05567cc618236d8ff5a02e))

## [0.5.0](https://github.com/briggleman/kraken/compare/v0.4.0...v0.5.0) (2026-07-08)


### Features

* **panel:** auto-sign Panel client cert so mTLS is on by default ([#21](https://github.com/briggleman/kraken/issues/21)) ([a3522d1](https://github.com/briggleman/kraken/commit/a3522d18a27ac8bf368e76a6c1eb4f11c978b51a))
* **web:** merge Wine into platform dropdown + tabbed agent-install docs ([#22](https://github.com/briggleman/kraken/issues/22)) ([737b027](https://github.com/briggleman/kraken/commit/737b0275462da705f5dc4f885418dcfa778f3b67))


### Bug Fixes

* **ci:** chain release-binaries + release-images inline from release-please ([#18](https://github.com/briggleman/kraken/issues/18)) ([c55e65d](https://github.com/briggleman/kraken/commit/c55e65d54496daeda5f5bee55c3a401406af12ee))

## [0.4.0](https://github.com/briggleman/kraken/compare/v0.3.1...v0.4.0) (2026-07-08)


### Features

* **agent:** auto-enroll co-located Agent so mTLS is on by default ([#16](https://github.com/briggleman/kraken/issues/16)) ([c4c62cb](https://github.com/briggleman/kraken/commit/c4c62cb2bf935aab53a937aec4cbbbe3e89240ca))


### Bug Fixes

* **server:** gate power actions on install state + add reinstall endpoint ([#14](https://github.com/briggleman/kraken/issues/14)) ([7512b52](https://github.com/briggleman/kraken/commit/7512b52b8cca5ed04beaa06168289164b2bd5c03))

## [0.3.1](https://github.com/briggleman/kraken/compare/v0.3.0...v0.3.1) (2026-07-08)


### Bug Fixes

* **agent:** refuse plaintext gRPC on non-loopback + safe-by-default binds ([#11](https://github.com/briggleman/kraken/issues/11)) ([2194f20](https://github.com/briggleman/kraken/commit/2194f20400461890b79892569468bc81d05a2137))

## [0.3.0](https://github.com/briggleman/kraken/compare/v0.2.0...v0.3.0) (2026-07-08)


### Features

* simplify deployment (single-binary, docker compose, install.sh) ([#7](https://github.com/briggleman/kraken/issues/7)) ([2c624e3](https://github.com/briggleman/kraken/commit/2c624e3f057914e288cb53bcbbeb30c3fd52f250))

## [0.2.0](https://github.com/briggleman/kraken/compare/v0.1.0...v0.2.0) (2026-07-07)


### Features

* **ci:** release-binaries workflow + social preview + branch guidance ([ccea13a](https://github.com/briggleman/kraken/commit/ccea13a1c5e1c3fdb34e730a9843c7f6d0cb7bed))
* **ci:** release-binaries workflow + social preview + branch guidance ([ccea13a](https://github.com/briggleman/kraken/commit/ccea13a1c5e1c3fdb34e730a9843c7f6d0cb7bed))
* **ci:** release-binaries workflow + social preview + branch guidance ([cbea48e](https://github.com/briggleman/kraken/commit/cbea48e885614b7d700167095d6f3c09d477cd54))
* expand catalog, redesign add-a-game wizard, and automate releases ([3cd2fbc](https://github.com/briggleman/kraken/commit/3cd2fbcc83832c7c04ecffd7310604beb271b12b))


### Bug Fixes

* **security:** resolve high-severity CodeQL alerts ([b8ebd0b](https://github.com/briggleman/kraken/commit/b8ebd0b9dd468345290301bcbb2e0933bcfdf509))
* **security:** resolve high-severity CodeQL alerts ([b8ebd0b](https://github.com/briggleman/kraken/commit/b8ebd0b9dd468345290301bcbb2e0933bcfdf509))
* **security:** resolve high-severity CodeQL alerts ([7ac3905](https://github.com/briggleman/kraken/commit/7ac3905ff689adee661ffe846b14ad1884a424c9))
