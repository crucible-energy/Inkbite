# HTML table preservation

Observed and implemented: 2026-10-01.

## User Experience Findings

Trufflehound's real document-reduction qualification found that a source HTML
table became concatenated text (`EvidenceStatusRaw sourceRetained`). Headings,
emphasis, and links survived, but row/column boundaries did not. A successful
conversion and byte-identical replay therefore did not establish table fidelity.

## Engineering Decisions

- The existing html-to-markdown v2.5.0 convenience function installs only its
  base and CommonMark plugins. Construct a converter with those same plugins
  plus its existing table plugin. No dependency or duplicated table parser is
  introduced.
- Create the converter per call, preserving the existing ownership/concurrency
  posture and script/style removal.
- Keep header promotion disabled by default: source data rows must not silently
  become asserted column headings. GFM needs a header/separator representation;
  a headerless source retains its original first row as data.
- Keep raw-source retention in the ingestion owner. This fixes a reduction
  defect; Markdown does not replace the original HTML or become lossless layout.

## Validation

- `go test -mod=readonly ./converters/html ./cmd/inkbite`: passed.
- `go test -mod=readonly ./...`: passed.
- Focused fixtures check header/separator/two-data-row boundaries, link and
  emphasis preservation inside cells, literal-pipe escaping, surrounding prose,
  and a headerless table's first row remaining data.

## Known Limitations

The check qualifies the named fixtures, not an arbitrary HTML corpus. Nested
tables, complex spans, visual layout, and all-format extraction still need
separate fidelity evidence. The owning library's span behavior remains in force;
no fabricated layout score, percentage-complete claim, or performance metric is
reported.

## Review follow-through: bounded table rendering

The review of <https://github.com/crucible-energy/Inkbite/pull/12> identified
numeric-span allocation and aligned-padding amplification paths in the newly
enabled dependency renderer. This was an actual dependency path, not a hosted
service or cross-tenant exposure claim.

The converter now bounds input/output at 32 MiB, token-driven DOM allocation,
DOM nodes at 65,536, DOM depth at 256, and positive spans at 32. Before the
dependency runs, a checked conservative rectangular-grid budget caps all table
expansion at 65,536 cells across the document. Excessive inputs fail with
`ErrHTMLLimit`; source spans are never silently rewritten into invented data.
The same effective first attribute used by the dependency governs budgeting.
Minimal padding prevents a single wide cell from padding every other row.

Context cancellation is checked during preflight/DOM/table traversal and passed
to the converter. Focused tests reject enormous individual/combined spans,
validate bounded wide-cell output, and check cancellation/depth failures. The
full `go test -mod=readonly ./...` suite passed after these controls.

The expansion estimate is deliberately conservative, so some complex legitimate
tables can exceed this profile. DOM parsing still uses the owning parser; these
controls are resource bounds, not a hostile-process sandbox or arbitrary-format
fidelity guarantee. Raw-source retention remains the ingestion owner's concern.
