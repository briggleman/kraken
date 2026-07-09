# Changelog

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
