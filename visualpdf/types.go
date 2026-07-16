// Package visualpdf compiles a local PDF into a verified, offline visual package.
//
// It is deliberately separate from Inkbite's Markdown conversion API.  The
// visual compiler is an opt-in build-time capability that requires a pinned
// Poppler installation and a profile-specific SVG renderer; the normal
// document-to-Markdown path stays self-contained.
package visualpdf

import "time"

const (
	// ManifestSchemaVersion is the version of the emitted visual package manifest.
	ManifestSchemaVersion = "inkbite.visualpdf.manifest.v3"
	// ProfileSetSchemaVersion is the version of a visual profile set document.
	ProfileSetSchemaVersion = "inkbite.visualpdf.profiles.v3"
	// CalibrationReportSchemaVersion is the version of a reviewed visual
	// calibration report referenced by a v3 profile.
	CalibrationReportSchemaVersion = "inkbite.visualpdf.calibration.v1"
	// VisualComparisonAlgorithm identifies the pixel comparison implemented by
	// this compiler. Calibration limits are valid only for this exact algorithm.
	VisualComparisonAlgorithm = "rgba-pixel-delta-v1"
)

// Toolchain identifies the build-time Poppler installation. Directory must be
// an absolute local directory containing pdfinfo, pdftocairo, and pdftotext.
// Version is checked against each tool's reported version.
type Toolchain struct {
	Directory string `json:"directory"`
	Version   string `json:"version"`
}

// WOFF2Subsetter identifies the optional build-time executable that produces
// source-font subsets. It must support --version and report Version exactly as
// a substring; without it, source-aware text remains unavailable.
type WOFF2Subsetter struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// SVGRenderer describes the qualified offline WebView-profile renderer used
// only during build verification. Arguments must contain exactly one {input}
// and one {output} placeholder. Width and height placeholders are optional.
// The renderer is intentionally supplied by the profile rather than silently
// falling back to a host browser or a network renderer.
type SVGRenderer struct {
	Path      string   `json:"path"`
	Version   string   `json:"version"`
	Arguments []string `json:"arguments"`
}

// ProfileCalibration is the only calibration material allowed in a v3 profile
// set. Numeric thresholds and review metadata are loaded from the pinned
// report rather than accepted from the profile itself.
type ProfileCalibration struct {
	Report       string `json:"report"`
	ReportSHA256 string `json:"report_sha256"`
	reportPath   string
	rootPath     string
	evidence     CalibrationEvidence
}

// CalibrationThresholds are the reviewed limits applied by the compiler.
type CalibrationThresholds struct {
	MaxChannelDelta  uint8 `json:"max_channel_delta"`
	MaxChangedPixels int   `json:"max_changed_pixels"`
}

// CalibrationCorpus identifies the committed calibration input that produced
// the reviewed thresholds.
type CalibrationCorpus struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Locator string `json:"locator"`
	SHA256  string `json:"sha256"`
}

// CalibrationReview records the explicit disposition of the calibration run.
type CalibrationReview struct {
	Outcome    string `json:"outcome"`
	ReviewedAt string `json:"reviewed_at"`
	ReviewedBy string `json:"reviewed_by"`
}

// CalibrationEvidence is persisted in every visual verification result.
type CalibrationEvidence struct {
	Comparator       string                `json:"comparator"`
	Report           string                `json:"report"`
	ReportSHA256     string                `json:"report_sha256"`
	ComparisonCorpus CalibrationCorpus     `json:"comparison_corpus"`
	Thresholds       CalibrationThresholds `json:"thresholds"`
	Review           CalibrationReview     `json:"review"`
}

// VisualProfile defines one deterministic reference render and matching SVG
// verification environment.
type VisualProfile struct {
	ID           string             `json:"id"`
	Version      string             `json:"version"`
	ReferenceDPI int                `json:"reference_dpi"`
	Renderer     SVGRenderer        `json:"svg_renderer"`
	Calibration  ProfileCalibration `json:"calibration"`
}

// ProfileSet is the versioned file format accepted by LoadProfileSet.
type ProfileSet struct {
	SchemaVersion string          `json:"schema_version"`
	Profiles      []VisualProfile `json:"profiles"`
}

// CompileOptions controls one complete local PDF package compilation.
type CompileOptions struct {
	InputPath       string
	OutputDirectory string
	Toolchain       Toolchain
	WOFF2Subsetter  *WOFF2Subsetter
	Profiles        []VisualProfile
	CompilerVersion string
	Now             func() time.Time
}

// Artifact is an integrity-addressed local output relative to the compiled
// package directory.
type Artifact struct {
	Locator   string `json:"locator"`
	MediaType string `json:"media_type"`
	ByteCount int64  `json:"byte_count"`
	SHA256    string `json:"sha256"`
}

// PageDimensions records PDF points rather than assuming a device pixel size.
type PageDimensions struct {
	WidthPoints  float64 `json:"width_points"`
	HeightPoints float64 `json:"height_points"`
}

// TextRun is source-derived text plus its PDF-space bounds. OCR is never a
// substitute for source PDF text in this package.
type TextRun struct {
	Text string  `json:"text"`
	XMin float64 `json:"x_min"`
	YMin float64 `json:"y_min"`
	XMax float64 `json:"x_max"`
	YMax float64 `json:"y_max"`
}

// CandidateState makes source-aware font-policy decisions explicit.
type CandidateState string

const (
	CandidateVerified    CandidateState = "verified"
	CandidateFailed      CandidateState = "failed_verification"
	CandidateUnavailable CandidateState = "unavailable"
)

// Candidate records a possible SVG primary display asset. InstalledByteCount
// includes only assets referenced by that candidate.
type Candidate struct {
	Kind               string         `json:"kind"`
	State              CandidateState `json:"state"`
	SVG                *Artifact      `json:"svg,omitempty"`
	ReferencedAssets   []Artifact     `json:"referenced_assets"`
	InstalledByteCount int64          `json:"installed_byte_count"`
	Verification       []Verification `json:"verification"`
	Reason             string         `json:"reason,omitempty"`
}

// Verification is a profile-specific visual gate result.
type Verification struct {
	ProfileID       string              `json:"profile_id"`
	ProfileVersion  string              `json:"profile_version"`
	Reference       Artifact            `json:"reference"`
	Rendered        *Artifact           `json:"rendered,omitempty"`
	Passed          bool                `json:"passed"`
	MaxChannelDelta uint8               `json:"max_channel_delta"`
	ChangedPixels   int                 `json:"changed_pixels"`
	Calibration     CalibrationEvidence `json:"calibration"`
	Reason          string              `json:"reason,omitempty"`
}

// PageState is the only display state an emitted page can have.
type PageState string

const (
	PageVerifiedSVG    PageState = "verified_svg"
	PageRasterFallback PageState = "raster_fallback"
	PageRejected       PageState = "rejected"
)

// RemediationItem is append-only, deterministic evidence for a page that did
// not become a verified SVG primary.
type RemediationItem struct {
	Page           int    `json:"page"`
	SourceSHA256   string `json:"source_sha256"`
	CompilerReason string `json:"compiler_reason"`
	FailedProfile  string `json:"failed_profile"`
}

// SourceRasterAsset preserves a painted PDF image XObject or its transparency
// mask independently from the SVG candidate. These sidecars preserve source
// JPEG bytes or lossless decoded pixels for fidelity review and future SVG
// asset placement; they are not a substitute for the verified display asset.
type SourceRasterAsset struct {
	Name       string                  `json:"name"`
	Role       string                  `json:"role"`
	MaskFor    string                  `json:"mask_for,omitempty"`
	Placements []SourceRasterPlacement `json:"placements"`
	Encoding   string                  `json:"encoding"`
	Artifact   Artifact                `json:"artifact"`
}

// SourceRasterPlacement records the PDF/SVG [a b c d e f] transform for each
// time a source image XObject was painted. A mask shares its image's placement
// until a verified SVG reconstruction can consume that relationship.
type SourceRasterPlacement struct {
	Matrix [6]float64 `json:"matrix"`
}

// PageManifest contains every display, semantic, and visual-verification
// artifact for a source PDF page.
type PageManifest struct {
	Page               int                 `json:"page"`
	Dimensions         PageDimensions      `json:"dimensions"`
	State              PageState           `json:"state"`
	PrimaryDisplay     *Artifact           `json:"primary_display,omitempty"`
	RasterFallback     *Artifact           `json:"raster_fallback,omitempty"`
	SemanticMarkdown   Artifact            `json:"semantic_markdown"`
	TextRuns           Artifact            `json:"text_runs"`
	SourceRasterAssets []SourceRasterAsset `json:"source_raster_assets"`
	Candidates         []Candidate         `json:"candidates"`
	RemediationState   string              `json:"remediation_state"`
}

// Manifest is the durable package contract consumed by downstream packagers.
type Manifest struct {
	SchemaVersion    string            `json:"schema_version"`
	CompilerVersion  string            `json:"compiler_version"`
	GeneratedAt      string            `json:"generated_at"`
	Source           Artifact          `json:"source"`
	Toolchain        Toolchain         `json:"toolchain"`
	WOFF2Subsetter   *WOFF2Subsetter   `json:"woff2_subsetter,omitempty"`
	Pages            []PageManifest    `json:"pages"`
	RemediationQueue []RemediationItem `json:"remediation_queue"`
}
