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
