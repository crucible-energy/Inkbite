# Changelog

All notable changes to this project will be documented in this file.

The format is intentionally lightweight at the current stage of the project.

## Unreleased

### Added

- managed optional-component foundation with `components list`, `doctor`,
  `config show`, and `install ocr`
- experimental `install ocr --provider paddleocr` path for managed CPU OCR
  runtime setup
- paddleocr installer polish with streamed progress, pinned `chardet`
  compatibility, and a faster quieter self-test path
- distributable Codex skill for guiding Inkbite CLI and library usage
- basic legacy XLS extraction with formatted numeric and date rendering
- reduced-scope PPTX extraction with support for slide order, slide titles,
  body text, notes, simple tables, and hyperlinks
- fixture-backed regression coverage for PDF, DOCX, EPUB, PPTX, and ZIP flows
- malformed-input regression tests for PDF, DOCX, EPUB, and PPTX
- ZIP archive guardrails for entry count, entry size, total uncompressed size,
  and recursion depth
- build automation through `Makefile`
- continuous integration workflow for test, vet, and CLI build verification
- release workflow for tagged builds and generated release notes
- cross-platform CI coverage for Linux, macOS, and Windows with race detection
  and automated `govulncheck` scanning
- packaged release archives for Linux, macOS, and Windows with generated
  checksum manifests
- optional Visual-PDF compiler for fidelity-gated, offline PDF packages with
  retained source PDFs, source-derived semantic artifacts, verified display
  assets, integrity manifests, and deterministic remediation records

### Changed

- PDF extraction is fully self-contained and no longer depends on external
  executables
- the pure-Go PDF path now handles array-valued page `/Contents` streams
  correctly, falls back to positioned page content when plain-text extraction
  returns an empty page, and preserves dense prose lines that contain repeated
  spacing instead of dropping them as false table candidates
- the pure-Go PDF path now restores `--keep-data-uris` raster extraction for
  rendered JPEG, Flate, CCITT, indexed-color, and image-mask PDF XObjects while
  skipping undeployed resource-only image masks that never appear in page draw
  operations
- legacy XLS extraction now uses a self-contained reader path with improved
  formatted output for common date and numeric cells
- README now documents the project in a formal, research-oriented tone
- remote HTTP fetches now enforce a bounded response size limit by default
- the module selects Go `1.26.5` and updates `golang.org/x/net`,
  `golang.org/x/image`, and `golang.org/x/crypto` to versions that fix the
  reachable standard-library and dependency vulnerability findings
- Visual-PDF SVG validation now parses the document and rejects every event
  attribute, processing instruction, XML directive, stylesheet import, and
  unlisted resource reference
- Visual-PDF compilation now stages output and publishes it only after the
  manifest is complete; malformed profile contracts and truncated raster data
  fail closed without leaving a partial package
- converter-registry snapshots are synchronized for concurrent registration
  and conversion
