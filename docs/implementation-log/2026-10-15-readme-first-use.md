# README first-use and implementation boundary

Owner: Reviewer Renata (`/root/candidate_custody_review`), under Codex Root's
organization README mission. Root owns independent review before merge.
Branch: `docs/readme-first-use`.
Baseline: [`7e9af8fd9eef1b29eb74a85a123264a6dd974916`](https://github.com/crucible-energy/Inkbite/tree/7e9af8fd9eef1b29eb74a85a123264a6dd974916).
History: the [existing README](../../README.md#abstract) and
[changelog](../../CHANGELOG.md) remain intact. This separate log follows
[Kaizen's pinned mission-log procedure](https://github.com/crucible-energy/kaizen/blob/4ca2672d97ffe34d9a7d9a4df3d6f8b6c93c8b51/docs/mission-log-procedure.md).

## User Experience Findings

2026-10-15: The README placed research framing before a practical entry point.
Its build command produced `bin/inkbite`, while subsequent examples assumed an
`inkbite` command already on `PATH`. New readers lacked an explicit local
invocation, toolchain/download prerequisites and expected first result.

The current Go implementation was described without the newly merged policy
requiring Zig for new repository-owned implementation. Existing source and
compatibility paths must remain distinct from that future implementation rule.

## Engineering Decisions

2026-10-15: Add one compact entry block and preserve every pre-existing README
byte after the title. Document source inspection, the existing Makefile build,
converter listing and local binary path. Keep OCR helper setup separate from
OCR-backed conversion, and preserve the visual-PDF profile's unqualified state.
Do not present `doctor` as read-only: its existing implementation can run a
helper self-test and update component configuration.

Use a second isolated documentation worktree from fetched `main`, within the
three-checkout limit. Preserve the stale primary checkout and its untracked
`.elastic-fog/` and `.env.example`; neither belongs to this slice. No competing
README worktree or open pull request was observed. The existing worktree alias
points to the primary rather than creating another checkout.

Validation uses the unchanged Go contract and documented `make ci` with
`GOMAXPROCS=2`. It adds no Go implementation or Zig qualification. Inspect normal
hooks, freeze README and this log before final gates, and retain exact results
in external program receipts and the pull request. Do not rewrite this frozen
log merely to append a passing result.

## Known Limitations

2026-10-15: No Zig replacement, OCR conversion wiring, qualified visual renderer,
format-fidelity improvement, model integration or service readiness is claimed.
Validation is limited to converter discovery and the existing build/test/vet
contract; it uses no private document, conversion, OCR setup or remote extraction.
The primary lane and its owner evidence remain protected. Root independently
reviews the exact tested candidate before any merge or workspace retirement.
