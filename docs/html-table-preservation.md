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

## 2026-10-03 — Keep rejection terminal through consumers

### User Experience Findings

The next review found RSS/Atom's existing raw-HTML fallback: a field rejected by
HTML preflight could become successful Markdown unchanged. Inspection also found
that default engine dispatch could retry a rejected HTML/feed as plain text, and
ZIP conversion could silently skip a rejected HTML member. A successful result
therefore did not prove the conversion controls had been respected.

### Engineering Decisions

- Propagate embedded HTML errors with feed-field context and return no partial
  success. Do not replace rejected content with a description/summary fallback.
  A successfully empty conversion stays empty; script/style-only content must not
  reappear simply because its reduction produced no text.
- Expose `inkbite.ErrResourceLimit` as a terminal dispatch error; `ErrHTMLLimit`
  wraps it while preserving `errors.Is(err, htmlconv.ErrHTMLLimit)`. Resource
  rejection and cancellation/deadline errors cannot fall through to another
  converter or disappear inside ZIP-member conversion. Ordinary converter
  failures retain the existing retry behavior.
- Pass caller context through existing HTML `Convert` in RSS/Atom and EPUB rather
  than adding another public string-conversion API.

### Validation

Focused fixtures cover all six HTML-bearing feed-field paths, empty/script-only
output, normal table retention, cancellation, and default-engine HTML/RSS/Atom/XML
and ZIP-member dispatch. They assert the rejection identity and absence of raw or
partial output, not a coverage percentage.

### Known Limitations

Feed XML/EPUB/archive acquisition and parsing have separate resource behavior;
these HTML controls are not a universal archive or feed-memory ceiling. Existing
reader calls are not interruptible while blocked. HTML reduction is not a general
HTML sanitization service or a promise about executable downstream rendering.
