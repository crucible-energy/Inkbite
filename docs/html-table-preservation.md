# HTML table preservation

Observed and implemented: 2026-10-01.

Current continuation: 2026-10-05; the final section updates the span/expansion
guard while retaining raw-source authority and terminal resource rejection.

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

Review follow-through: terminal errors take precedence even when a registered
converter returns an aggregate matching both `ErrUnsupportedFormat` and a limit
or cancellation sentinel. Engine and ZIP regression fixtures use joined errors
to verify this precedence, preventing an unsupported-format check from masking
rejection. Full Go tests and `go vet` passed after the precedence change.

## 2026-10-05 — Admit bounded real filing tables without weakening rejection

### User Experience Findings

Trufflehound's retained SEC primary 10-K was rejected by the span-32 guard. Two
source cells declare `colspan="39"`; repeated small spans also exposed the old
estimator's multiplication of every modification by every row. That conservative
estimate rejected ordinary bounded table structure, not just unsafe expansion.

The candidate output also exposes concatenated hidden inline-XBRL context at its
start. That is a separate readability/semantic-reduction finding: successful
table rendering and exact replay do not establish visible-text or accounting
interpretation fidelity. The original source remains in the ingestion owner.

### Engineering Decisions

- Model the pinned html-to-markdown v2.5.0 renderer's header/row selection,
  reverse modification groups, grow-before-insert and final rectangular padding
  with **row lengths only**. No expanded cell payloads are allocated during
  preflight and no source spans or data are rewritten to force success.
- Keep the document-wide 65,536-cell grid budget, bounded token/DOM/depth/input/
  output profile, minimal padding and typed terminal errors. Positive spans are
  individually bounded by that cell ceiling and checked area arithmetic prevents
  overflow before multiplication. They must fit the actual aggregate grid.
- Bound expanded modification count at 65,536 and insertion shift work at
  2,097,152 cell references per table before entering the dependency. The latter
  prevents a small final rectangle hiding expensive overlapping inserts. These
  are enforceable budgets, not throughput or energy measurements.
- Empty-row removal and nested-table fallback are ignored conservatively in
  budgeting. The model is version-specific; a dependency upgrade needs renewed
  selector/expansion parity checks. Invocation/ownership stays scoped per call.
- Feed/dispatch terminal-rejection fixtures now use a genuinely over-budget
  span (`65537`), because span `33` is valid under the new aggregate guard. All
  rejection/no-raw-fallback assertions remain in force.

### Validation

- Full `go test -mod=readonly ./...` and `go vet -mod=readonly ./...` passed.
- Meaningful new fixtures retain span-39 headings and 300 repeated bounded rows,
  reject extreme spans, cumulative document grids and dense insertion work, and
  compare computed footprints with actual rendered overlapping/header grids.
- Trufflehound's actual candidate CLI reduced the **same original primary source**
  into `document-aa8d68f1ac9bb207d5327b3ee702e13c`; schema, source proof,
  independent verification and byte-identical replay passed. The old rejected
  document remains retained. Candidate binary SHA-256:
  `74cb8df4dc0648727616c047b2bdfc666311927fbb01dbd2b974c707abad5f5e`.
  Document-manifest SHA-256:
  `6ea388d1d276dd47b9e56acffe4455be2546820e7d68fee40a428b943e162dea`.
  This is one named representation/replay proof, not a corpus-fidelity sample.

### Known Limitations

This does not add OCR, resolve publisher byte-count discrepancies, sanitize all
HTML, reconstruct arbitrary nested layout or certify financial/legal semantics.
The context prefix and complex-table reduction need separate quality work. The
existing parser/dependency/executable trust and blocked-reader limits remain;
these are bounded conversion guards, not a hosted sandbox or deployment claim.
