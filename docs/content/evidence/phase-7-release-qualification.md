---
title: Phase 7 release qualification
---

Release qualification is in progress. The enterprise gate is recorded in [[phase-6-acceptance]]. This page tracks what the release checks establish for both SDKs.

| Gate | Verified evidence and outstanding checks |
| --- | --- |
| #41 and #42 Reference integrations | [[phase-4-reference-backends#phase-7-verification]] records invoice approval and ticket assignment. Clean-checkout quickstarts, explicit Acme/Globex role coverage and business-write outage checks still need release evidence. |
| #43 Security and failure suite | The workspace checks pass. Release qualification must map the live and deterministic tests to each first-release dry run and verify the live CI path. |
| #49 Mixed SDK versions and rollback | [[phase-3-acceptance]] covers storage compatibility. [[phase-7-model-rollout]] verifies staged model rollout and rollback with the current TS and Go implementations for both products. Mixed release artifacts and provisioning compatibility reporting still need evidence. |
| #50 Release artifacts | The packed ESM TypeScript SDK installs in an isolated project with the 30-day age rule. `sh scripts/check-go-consumer.sh 3becec4ec0ef2ac9fb209d765611ff73bef5d670` resolved the GitHub module as `v0.0.0-20261114040723-3becec4ec0ef` and compiled and ran a clean consumer importing core, PostgreSQL and provisioning. Tagged candidates, consumer journeys, publishing and release notes still need verification. |
| #55 Adapter safety | Prior phases cover retained audit and session parity, recovery, managed administration and discovery repair. Release qualification still needs the mixed-version adapter, recovery, backup and consumer-wiring evidence audit. |
