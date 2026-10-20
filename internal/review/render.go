package review

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// maxFieldLen bounds one rendered free-text field. Truncation keeps the
// bundle compact and is deterministic (fixed length, fixed ellipsis).
const maxFieldLen = 240

// clean applies the run package's secret redaction to one free-text value.
// Redaction is idempotent (the placeholder never matches the detectors), so
// applying it to content that already crossed the store's sanitization
// boundary is a no-op on clean input and a backstop on anything else: the
// rendered bundle never contains a detected secret, regardless of which
// store served the content.
func clean(s string) string {
	return run.RedactSecrets(s)
}

// esc prepares one free-text value for a single-line Markdown context
// (table cell, list item, blockquote): redact, escape pipes, flatten
// newlines, and bound the length.
func esc(s string) string {
	s = strings.ReplaceAll(clean(s), "|", `\|`)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	return trunc(s)
}

// escBlock prepares one free-text value for a blockquote context: redact,
// escape pipes, flatten CR, keep line breaks as <br>, and bound the total
// length.
func escBlock(s string) string {
	s = strings.ReplaceAll(clean(s), "|", `\\|`)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", "<br>")
	s = strings.Join(strings.Fields(s), " ")
	return trunc(s)
}

// trunc bounds a string deterministically.
func trunc(s string) string {
	if len(s) <= maxFieldLen {
		return s
	}
	return s[:maxFieldLen] + "…"
}

// link renders a URL as a Markdown link whose visible text is the URL
// without scheme, or the whole URL when it is not http(s).
func link(u string) string {
	u = clean(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		text := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		if len(text) > 80 {
			text = text[:80] + "…"
		}
		if text == "" {
			return "`" + u + "`"
		}
		return fmt.Sprintf("[%s](%s)", text, u)
	}
	return "`" + u + "`"
}

// Render produces the review bundle as a Markdown document. For a given
// bundle the output is byte-identical (no timestamps, no map iteration
// order, no randomness): the document is a pure function of the run's
// persisted record.
func (b *Bundle) Render() string {
	var out strings.Builder
	b.renderHeader(&out)
	b.renderIssues(&out)
	b.renderPipeline(&out)
	b.renderSources(&out)
	b.renderResearch(&out)
	b.renderClaims(&out)
	b.renderFollowUp(&out)
	b.renderFinalCheck(&out)
	b.renderArtifact(&out)
	b.renderOutput(&out)
	b.renderEvidence(&out)
	b.renderLogs(&out)
	b.renderFooter(&out)
	return out.String()
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

func (b *Bundle) renderHeader(out *strings.Builder) {
	fmt.Fprintf(out, "# Run Review: %s\n\n", esc(b.Topic))

	status := strings.ToUpper(string(b.Status))
	switch b.Status {
	case run.RunStatusSucceeded:
		if b.FinalCheckPresent && b.FinalCheckStatus == contracts.FactualCheckStatusPass {
			fmt.Fprintf(out, "> **Status: %s** — the final factual check passed; the output below is ready for local use.\n\n", status)
		} else {
			// A "succeeded" run without a passing check is inconsistent and
			// must never be presented as ready.
			fmt.Fprintf(out, "> **Status: %s** — the final factual check is missing or did not pass; treat the output as **NOT READY**.\n\n", status)
		}
	case run.RunStatusFailed:
		where := ""
		if b.FailedStage != "" {
			where = " (stopped at stage " + esc(string(b.FailedStage)) + ")"
		}
		fmt.Fprintf(out, "> **Status: %s**%s — output is **NOT READY**.\n\n", status, where)
	case run.RunStatusCancelled:
		fmt.Fprintf(out, "> **Status: %s** — the run was interrupted; output is **NOT READY**.\n\n", status)
	default:
		fmt.Fprintf(out, "> **Status: %s** — this run has not reached a terminal state; every section below is partial.\n\n", status)
	}

	out.WriteString("| | |\n|---|---|\n")
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(out, "| **%s** | %s |\n", k, v)
		}
	}
	row("Run ID", "`"+esc(string(b.RunID))+"`")
	row("Workspace", esc(b.WorkspaceName))
	row("Source mode", esc(string(b.SourceMode)))
	started := b.StartedAt.String()
	if started != "" {
		if t := duration(b.StartedAt.Time(), firstNonZero(b.FinishedAt, b.UpdatedAt)); t != "" {
			row("Duration", t)
		}
	}
	if b.FinishedAt != nil && b.FinishedAt.String() != "" {
		row("Finished", b.FinishedAt.String())
	}
	row("Profile", profileLine(b))
	row("Model", modelLine(b))
	row("Output format", configValue(b, formatKey))
	row("Quality threshold", configValue(b, qualityKey))
	if b.WorkspacePath != "" {
		row("Run directory", "`"+esc(b.RunDir)+"`")
	}
	out.WriteString("\n")
}

func firstNonZero(fin *run.Timestamp, upd run.Timestamp) time.Time {
	if fin != nil && !fin.IsZero() {
		return fin.Time()
	}
	return upd.Time()
}

func profileLine(b *Bundle) string {
	if b.ProfileID == "" {
		return "(none)"
	}
	line := esc(b.ProfileID)
	if b.ProfileHash != "" {
		line += " (hash " + esc(b.ProfileHash) + ")"
	}
	if b.ProfilePath != "" {
		line += " — " + esc(b.ProfilePath)
	}
	return line
}

func modelLine(b *Bundle) string {
	if b.ModelProvider == "" && b.ModelName == "" && b.ModelEndpoint == "" {
		return "(unrecorded)"
	}
	parts := []string{}
	if b.ModelProvider != "" {
		parts = append(parts, esc(b.ModelProvider))
	}
	if b.ModelName != "" {
		parts = append(parts, esc(b.ModelName))
	}
	line := strings.Join(parts, " / ")
	if b.ModelEndpoint != "" {
		if line != "" {
			line += " @ "
		}
		line += "`" + esc(b.ModelEndpoint) + "`"
	}
	return line
}

// ---------------------------------------------------------------------------
// Unresolved issues (prominent)
// ---------------------------------------------------------------------------

func (b *Bundle) renderIssues(out *strings.Builder) {
	out.WriteString("## Unresolved issues\n\n")

	type issue struct{ severity, text string }
	var issues []issue
	add := func(sev, text string) {
		if text != "" {
			issues = append(issues, issue{sev, text})
		}
	}

	if b.FinalCheckPresent && b.FinalCheckStatus != contracts.FactualCheckStatusPass {
		add("critical", fmt.Sprintf("Final factual check: **%s** (%d finding(s)) — the output must not be used until the findings are addressed",
			strings.ToUpper(string(b.FinalCheckStatus)), len(b.Findings)))
	}
	if b.ClaimsContradicted > 0 {
		add("critical", fmt.Sprintf("%d claim(s) contradicted by evidence", b.ClaimsContradicted))
	}
	if n := b.ClaimsUncertain + b.ClaimsInsufficient; n > 0 {
		add("high", fmt.Sprintf("%d claim(s) not fully supported (uncertain or insufficient evidence)", n))
	}
	if b.Status == run.RunStatusFailed {
		where := "the run failed"
		if b.FailedStage != "" {
			where = "the run failed at stage " + esc(string(b.FailedStage))
		}
		add("critical", where)
		if b.Reason != "" {
			add("critical", esc(b.Reason))
		}
	}
	if b.Status == run.RunStatusCancelled {
		add("high", "the run was cancelled before completion")
	}
	if b.QualityScoreSet && b.QualityScore < qualityThreshold(b) && !b.hasWarningCode("quality.low") {
		add("medium", fmt.Sprintf("verification quality score %.1f is below the %.1f gate", b.QualityScore, qualityThreshold(b)))
	}
	if n := len(b.Warnings); n > 0 {
		add("medium", fmt.Sprintf("%d run warning(s) recorded", n))
	}
	if n := len(b.BuildNotes); n > 0 {
		add("medium", fmt.Sprintf("%d bundle integrity note(s)", n))
	}
	if len(b.OutputMissing) > 0 {
		add("medium", fmt.Sprintf("%d rendered output file(s) missing from disk", len(b.OutputMissing)))
	}

	if len(issues) == 0 {
		var okText string
		if b.Verified {
			okText = fmt.Sprintf("All %d claim(s) are supported by evidence and the final factual check passed.", b.ClaimsTotal)
		} else if b.DossierPresent {
			okText = "Research completed, but verification was never reached."
		} else {
			okText = "No verification evidence is available for this run."
		}
		fmt.Fprintf(out, "> No unresolved issues. %s\n\n", okText)
		return
	}

	out.WriteString("| Severity | Issue |\n|---|---|\n")
	for _, is := range issues {
		fmt.Fprintf(out, "| %s | %s |\n", is.severity, is.text)
	}
	out.WriteString("\n")

	if n := len(b.Contradicted); n > 0 {
		fmt.Fprintf(out, "### Contradicted claims (%d)\n\n", n)
		for _, c := range b.Contradicted {
			renderClaim(out, c)
		}
		out.WriteString("\n")
	}
	if n := len(b.Unresolved); n > 0 {
		fmt.Fprintf(out, "### Unresolved claims (%d)\n\n", n)
		for _, c := range b.Unresolved {
			renderClaim(out, c)
		}
		out.WriteString("\n")
	}
	if n := len(b.Warnings); n > 0 {
		fmt.Fprintf(out, "### Run warnings (%d)\n\n", n)
		for _, w := range b.Warnings {
			fmt.Fprintf(out, "- `%s` — %s (%s)\n", esc(w.Code), esc(w.Message), w.OccurredAt.String())
		}
		out.WriteString("\n")
	}
	if n := len(b.BuildNotes); n > 0 {
		fmt.Fprintf(out, "### Bundle integrity notes (%d)\n\n", n)
		for _, note := range b.BuildNotes {
			fmt.Fprintf(out, "- %s\n", esc(note))
		}
		out.WriteString("\n")
	}
}

func renderClaim(out *strings.Builder, c ClaimInfo) {
	fmt.Fprintf(out, "- `%s` [%s] — %s\n", esc(c.ID), esc(string(c.Status)), escBlock(c.Statement))
	if c.HasConflicts || c.Limitations != "" {
		detail := ""
		if c.Limitations != "" {
			detail = esc(c.Limitations)
		}
		if c.HasConflicts {
			if detail != "" {
				detail += "; "
			}
			detail += "conflicting evidence recorded"
		}
		fmt.Fprintf(out, "  - %s\n", detail)
	}
}

// ---------------------------------------------------------------------------
// Pipeline
// ---------------------------------------------------------------------------

func (b *Bundle) renderPipeline(out *strings.Builder) {
	if len(b.Stages) == 0 {
		return
	}
	out.WriteString("## Pipeline\n\n")
	out.WriteString("| Stage | Status | Attempts | Note |\n|---|---|---|---|\n")
	for _, s := range b.Stages {
		note := ""
		switch s.Status {
		case run.StageStatusSkipped:
			note = esc(s.SkipReason)
		case run.StageStatusFailed:
			note = esc(s.Error)
		case run.StageStatusCancelled:
			note = "interrupted"
		}
		fmt.Fprintf(out, "| %s | %s | %d | %s |\n",
			"`"+string(s.Name)+"`", string(s.Status), s.Attempts, note)
	}
	out.WriteString("\n")
}

// ---------------------------------------------------------------------------
// Sources
// ---------------------------------------------------------------------------

func (b *Bundle) renderSources(out *strings.Builder) {
	if len(b.SourcesSelected) == 0 && len(b.FetchResults) == 0 {
		return
	}
	out.WriteString("## Sources\n\n")
	if len(b.SourcesSelected) > 0 {
		line := ""
		if b.SourceMode != "" {
			line = fmt.Sprintf("Mode: %s — ", esc(string(b.SourceMode)))
		}
		fmt.Fprintf(out, "%s%d source(s) selected", line, len(b.SourcesSelected))
		if b.Candidates > len(b.SourcesSelected) {
			fmt.Fprintf(out, " (from %d candidate(s))", b.Candidates)
		}
		out.WriteString("\n\n")
	}
	if len(b.Queries) > 0 {
		out.WriteString("Search queries: ")
		for i, q := range b.Queries {
			if i > 0 {
				out.WriteString(", ")
			}
			fmt.Fprintf(out, "`%s`", esc(q))
		}
		out.WriteString("\n\n")
	}
	if len(b.FetchResults) > 0 {
		fmt.Fprintf(out, "Fetched: %d of %d succeeded%s\n\n",
			b.FetchSuccesses, len(b.FetchResults), failedSuffix(b))
		out.WriteString("| Source | URL | Retrieval |\n|---|---|---|\n")
		for _, f := range b.FetchResults {
			label := f.Title
			if label == "" {
				label = f.URL
			}
			fmt.Fprintf(out, "| %s | %s | %s |\n",
				esc(label), link(f.URL), fetchOutcome(f))
		}
		out.WriteString("\n")
	}
	if b.DocumentsCount > 0 {
		fmt.Fprintf(out, "Documents extracted: %d\n\n", b.DocumentsCount)
	}
}

func failedSuffix(b *Bundle) string {
	if b.FetchFailed > 0 {
		return fmt.Sprintf(", %d failed", b.FetchFailed)
	}
	return ""
}

func fetchOutcome(f SourceFetch) string {
	if f.OK {
		if f.Status > 0 {
			return "ok (HTTP " + strconv.Itoa(f.Status) + ")"
		}
		return "ok"
	}
	msg := esc(f.Error)
	if msg == "" {
		msg = "failed"
	}
	if f.Status > 0 {
		return "HTTP " + strconv.Itoa(f.Status) + " — " + msg
	}
	return msg
}

// ---------------------------------------------------------------------------
// Research
// ---------------------------------------------------------------------------

func (b *Bundle) renderResearch(out *strings.Builder) {
	if !b.DossierPresent {
		return
	}
	out.WriteString("## Research dossier\n\n")
	fmt.Fprintf(out, "Claims produced: %d\n\n", b.DossierClaims)
	if len(b.DossierSources) > 0 {
		fmt.Fprintf(out, "Sources used (%d):\n\n", len(b.DossierSources))
		sorted := append([]researcher.SourceReference(nil), b.DossierSources...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].StableID < sorted[j].StableID })
		for _, s := range sorted {
			line := s.Title
			if line == "" {
				line = s.OriginalURL
			}
			fmt.Fprintf(out, "- %s — %s\n", esc(line), link(s.OriginalURL))
		}
		out.WriteString("\n")
	}
}

// ---------------------------------------------------------------------------
// Claims & verification
// ---------------------------------------------------------------------------

func (b *Bundle) renderClaims(out *strings.Builder) {
	if !b.Verified && b.DossierClaims == 0 {
		return
	}
	out.WriteString("## Claims & verification\n\n")
	if b.DossierClaims > 0 && b.ClaimsTotal == 0 {
		fmt.Fprintf(out, "The dossier contains %d claim(s); the verification stage was never reached.\n\n", b.DossierClaims)
		return
	}
	if !b.Verified {
		return
	}
	out.WriteString("| Status | Claims |\n|---|---|\n")
	fmt.Fprintf(out, "| supported | %d |\n", b.ClaimsSupported)
	fmt.Fprintf(out, "| contradicted | %d |\n", b.ClaimsContradicted)
	fmt.Fprintf(out, "| uncertain | %d |\n", b.ClaimsUncertain)
	fmt.Fprintf(out, "| insufficient evidence | %d |\n", b.ClaimsInsufficient)
	fmt.Fprintf(out, "| **total** | **%d** |\n\n", b.ClaimsTotal)
	if b.QualityScoreSet {
		fmt.Fprintf(out, "Quality score: %.1f / 100\n\n", b.QualityScore)
	}
	if b.ContradictionCount > 0 {
		fmt.Fprintf(out, "Documented contradictions: %d\n\n", b.ContradictionCount)
	}
	if b.VerificationSummary != "" {
		fmt.Fprintf(out, "Verifier summary: %s\n\n", escBlock(b.VerificationSummary))
	}
}

// ---------------------------------------------------------------------------
// Follow-up
// ---------------------------------------------------------------------------

func (b *Bundle) renderFollowUp(out *strings.Builder) {
	if !b.FollowUpPresent && b.FollowUpSkippedReason == "" {
		return
	}
	out.WriteString("## Follow-up research\n\n")
	if !b.FollowUpPresent {
		fmt.Fprintf(out, "Skipped: %s\n\n", esc(b.FollowUpSkippedReason))
		return
	}
	fmt.Fprintf(out, "Rounds: %d · queries: %d · sources fetched: %d\n\n",
		b.FollowUpRounds, b.FollowUpQueries, b.FollowUpSources)
	if n := len(b.FollowUpResolved); n > 0 {
		fmt.Fprintf(out, "Resolved: %s\n\n", idsLine(b.FollowUpResolved))
	}
	if n := len(b.FollowUpUnresolved); n > 0 {
		fmt.Fprintf(out, "Still unresolved: %s\n\n", idsLine(b.FollowUpUnresolved))
	} else if b.FollowUpResolved != nil || b.FollowUpRounds > 0 {
		fmt.Fprintf(out, "Still unresolved: none\n\n")
	}
	if n := len(b.FollowUpWarnings); n > 0 {
		out.WriteString("Warnings:\n")
		for _, w := range b.FollowUpWarnings {
			fmt.Fprintf(out, "- %s\n", esc(w))
		}
		out.WriteString("\n")
	}
	if n := len(b.FollowUpErrors); n > 0 {
		out.WriteString("Errors:\n")
		for _, e := range b.FollowUpErrors {
			fmt.Fprintf(out, "- %s\n", esc(e))
		}
		out.WriteString("\n")
	}
}

func idsLine(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "`"+esc(id)+"`")
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// Final factual check
// ---------------------------------------------------------------------------

func (b *Bundle) renderFinalCheck(out *strings.Builder) {
	if !b.FinalCheckPresent {
		return
	}
	out.WriteString("## Final factual check\n\n")
	status := strings.ToUpper(string(b.FinalCheckStatus))
	verdict := ""
	switch b.FinalCheckStatus {
	case contracts.FactualCheckStatusPass:
		verdict = " — the artifact passed the final gate"
	case contracts.FactualCheckStatusFail:
		verdict = " — **the artifact failed the final gate and must not be used**"
	case contracts.FactualCheckStatusReview:
		verdict = " — **human review is required before use**"
	}
	fmt.Fprintf(out, "Overall: **%s**%s · confidence: %s\n\n", status, verdict, esc(string(b.FinalCheckConfidence)))
	if b.FinalCheckSummary != "" {
		fmt.Fprintf(out, "%s\n\n", escBlock(b.FinalCheckSummary))
	}
	if n := len(b.Findings); n > 0 {
		fmt.Fprintf(out, "Findings (%d):\n\n", n)
		for _, f := range b.Findings {
			id := f.FindingID
			if id == "" {
				id = "finding"
			}
			claims := ""
			if len(f.RelatedClaimIDs) > 0 {
				claims = " (claims " + idsLine(f.RelatedClaimIDs) + ")"
			}
			fmt.Fprintf(out, "- `%s` [%s / %s / %s] %s%s\n",
				esc(id), esc(string(f.Severity)), esc(string(f.FindingType)), esc(string(f.Category)), esc(f.Statement), claims)
		}
		out.WriteString("\n")
	}
	b.renderCheckCategory(out, "Unsupported statements",
		func(i int) string {
			return fmt.Sprintf("- [%s] %s", esc(string(b.Unsupported[i].Severity)), esc(b.Unsupported[i].Statement))
		})
	b.renderCheckCategory(out, "Altered details",
		func(i int) string {
			return fmt.Sprintf("- [%s / %s] %s → %s",
				esc(string(b.Altered[i].DetailType)), esc(string(b.Altered[i].Significance)),
				esc(b.Altered[i].OriginalValue), esc(b.Altered[i].AlteredImageValue))
		})
	b.renderCheckCategory(out, "Out-of-context statements",
		func(i int) string {
			return fmt.Sprintf("- [%s] %s", esc(string(b.OutOfContext[i].Severity)), esc(b.OutOfContext[i].Statement))
		})
	if n := len(b.Recommendations); n > 0 {
		fmt.Fprintf(out, "Recommendations (%d):\n\n", n)
		for _, r := range b.Recommendations {
			fmt.Fprintf(out, "- [%s / %s] %s\n", esc(string(r.Priority)), esc(string(r.RecommendationType)), esc(r.Description))
		}
		out.WriteString("\n")
	}
}

// renderCheckCategory prints one raw-finding category when non-empty.
func (b *Bundle) renderCheckCategory(out *strings.Builder, title string, line func(i int) string) {
	n := countCategory(b, title)
	if n == 0 {
		return
	}
	fmt.Fprintf(out, "%s (%d):\n\n", title, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(out, "%s\n", line(i))
	}
	out.WriteString("\n")
}

func countCategory(b *Bundle, title string) int {
	switch title {
	case "Unsupported statements":
		return len(b.Unsupported)
	case "Altered details":
		return len(b.Altered)
	case "Out-of-context statements":
		return len(b.OutOfContext)
	}
	return 0
}

// ---------------------------------------------------------------------------
// Editorial artifact
// ---------------------------------------------------------------------------

func (b *Bundle) renderArtifact(out *strings.Builder) {
	if !b.ArtifactPresent {
		return
	}
	out.WriteString("## Editorial artifact\n\n")
	title := b.ArtifactTitle
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(out, "%s (%s)\n\n", esc(title), esc(string(b.ArtifactType)))
	if b.ArtifactClaims > 0 {
		fmt.Fprintf(out, "Claim references: %d\n\n", b.ArtifactClaims)
	}
	if n := len(b.ArtifactWarnings); n > 0 {
		fmt.Fprintf(out, "Warnings (%d):\n\n", n)
		for _, w := range b.ArtifactWarnings {
			fmt.Fprintf(out, "- [%s] %s\n", esc(string(w.Severity)), esc(w.Message))
		}
		out.WriteString("\n")
	}
}

// ---------------------------------------------------------------------------
// Output (readiness statement + rendered files)
// ---------------------------------------------------------------------------

func (b *Bundle) renderOutput(out *strings.Builder) {
	out.WriteString("## Output artifacts\n\n")

	ready, why := Readiness(b)
	if ready {
		fmt.Fprintf(out, "> **READY** — %s\n\n", why)
	} else {
		fmt.Fprintf(out, "> **NOT READY** — %s\n\n", why)
	}

	if len(b.OutputPaths) == 0 {
		fmt.Fprintf(out, "No rendered output was produced for this run.\n\n")
		return
	}
	for _, p := range b.OutputPaths {
		marker := "ok"
		if b.outputIsMissing(p) {
			marker = "**missing**"
		}
		fmt.Fprintf(out, "- `%s` — %s\n", esc(p), marker)
	}
	out.WriteString("\n")
}

// Readiness decides whether the run's output may be presented as ready.
//
// The rule is deliberately strict: only a succeeded run whose final
// factual check passed is ever described as ready; everything else
// (failed gate, failed run, cancelled run, missing check) is not.
//
// This is the single readiness rule of the review model: the rendered
// review bundle and the v0.6 publication gate (internal/publish) apply
// the exact same decision, so what a human is told and what a
// publisher is allowed to do never diverge.
func Readiness(b *Bundle) (bool, string) {
	if b.Status != run.RunStatusSucceeded {
		switch b.Status {
		case run.RunStatusFailed:
			if b.FailedStage != "" {
				return false, fmt.Sprintf("the run failed at stage %s; the pipeline did not complete", esc(string(b.FailedStage)))
			}
			return false, "the run failed; the pipeline did not complete"
		case run.RunStatusCancelled:
			return false, "the run was cancelled before completion"
		default:
			return false, "the run has not finished"
		}
	}
	// Succeeded run: the gate is the final factual check.
	if !b.FinalCheckPresent {
		return false, "no final factual-check result is recorded for this run"
	}
	switch b.FinalCheckStatus {
	case contracts.FactualCheckStatusPass:
		if len(b.OutputMissing) > 0 {
			return false, "the final check passed but rendered output is missing from the run directory"
		}
		return true, "the run completed and the final factual check passed; use the output locally (nothing was published)"
	case contracts.FactualCheckStatusFail:
		return false, fmt.Sprintf("the final factual check **failed** (%d finding(s)) — do not use the output until the findings are addressed", len(b.Findings))
	case contracts.FactualCheckStatusReview:
		return false, "the final factual check requires human review — the output is not cleared for use"
	default:
		return false, "the final factual check result is uninterpretable"
	}
}

func (b *Bundle) outputIsMissing(p string) bool {
	for _, m := range b.OutputMissing {
		if m == p {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Evidence files (the files a reviewer opens from the run directory)
// ---------------------------------------------------------------------------

func (b *Bundle) renderEvidence(out *strings.Builder) {
	if b.RunDir == "" {
		return
	}
	type ev struct{ name, path string }
	evidence := []ev{
		{"run manifest", manifestFileName},
		{"source selection", run.ArtifactPathSourcesIndex},
		{"fetch results", run.ArtifactPathFetchIndex},
		{"documents", run.ArtifactPathDocumentsIndex},
		{"research dossier", run.ArtifactPathDossier},
		{"verification", run.ArtifactPathVerification},
		{"follow-up result", run.ArtifactPathFollowUp},
		{"editorial artifact", run.ArtifactPathArtifact},
		{"final check", run.ArtifactPathFinalCheck},
	}
	// Only list files the run could plausibly have written (in plan order,
	// bounded by what the stage statuses allow), plus the manifest itself.
	planned := map[run.StageName]bool{}
	for _, s := range b.Stages {
		planned[s.Name] = true
	}
	out.WriteString("## Evidence files\n\n")
	fmt.Fprintf(out, "Run directory: `%s`\n\n", esc(b.RunDir))
	out.WriteString("| Evidence | Path | On disk |\n|---|---|---|\n")
	for _, e := range evidence {
		if e.path != manifestFileName {
			stage := stageForEvidencePath(e.path)
			if stage != "" && !planned[stage] {
				continue
			}
			if stage == run.StageFollowUp && !b.FollowUpPresent && b.FollowUpSkippedReason != "" {
				continue
			}
		}
		full := filepath.Join(b.RunDir, filepath.FromSlash(e.path))
		marker := "yes"
		if !exists(full) {
			marker = "no"
		}
		fmt.Fprintf(out, "| %s | `%s` | %s |\n", e.name, e.path, marker)
	}
	for _, p := range b.OutputPaths {
		marker := "yes"
		if b.outputIsMissing(p) {
			marker = "no"
		}
		fmt.Fprintf(out, "| rendered output | `%s` | %s |\n", esc(p), marker)
	}
	out.WriteString("\n")
}

// stageForEvidencePath maps a canonical evidence path to the stage that
// wrote it ("" for the manifest, which is not a stage artifact).
func stageForEvidencePath(path string) run.StageName {
	switch path {
	case run.ArtifactPathSourcesIndex:
		return run.StageDiscovery
	case run.ArtifactPathFetchIndex:
		return run.StageFetch
	case run.ArtifactPathDocumentsIndex:
		return run.StageExtract
	case run.ArtifactPathDossier:
		return run.StageResearch
	case run.ArtifactPathVerification:
		return run.StageVerify
	case run.ArtifactPathFollowUp:
		return run.StageFollowUp
	case run.ArtifactPathArtifact:
		return run.StageWrite
	case run.ArtifactPathFinalCheck:
		return run.StageFinalCheck
	}
	return ""
}

// ---------------------------------------------------------------------------
// Run logs
// ---------------------------------------------------------------------------

func (b *Bundle) renderLogs(out *strings.Builder) {
	if len(b.Errors) == 0 {
		return
	}
	out.WriteString("## Run errors\n\n")
	for _, e := range b.Errors {
		code := ""
		if e.Code != "" {
			code = "`" + esc(e.Code) + "` — "
		}
		fmt.Fprintf(out, "- %s%s (%s)\n", code, esc(e.Message), e.OccurredAt.String())
	}
	out.WriteString("\n")
}

// ---------------------------------------------------------------------------
// Footer
// ---------------------------------------------------------------------------

func (b *Bundle) renderFooter(out *strings.Builder) {
	out.WriteString("---\n\n")
	fmt.Fprintf(out,
		"*Generated deterministically by local-newsroom v0.5 (no LLM calls). Regenerate with `newsroom review %s`.*\n",
		esc(string(b.RunID)))
}

// ---------------------------------------------------------------------------
// Config snapshot helpers
// ---------------------------------------------------------------------------

// Canonical run-level snapshot keys (mirrors the orchestrator's settings).
const (
	formatKey     = "run.format"
	followUpKey   = "run.follow_up"
	qualityKey    = "run.quality_threshold"
	languageKey   = "run.language"
	maxSourcesKey = "run.max_sources"

	// manifestFileName is the manifest file name inside the run directory
	// (the workspace layout constant; review must not import workspace).
	manifestFileName = "manifest.json"
)

// defaultQualityGate is the verification quality gate the run command applies
// when the run did not record its own.
const defaultQualityGate = 60.0

// qualityThreshold returns the run's verification quality gate: the recorded
// snapshot value when present and parseable, otherwise the default.
func qualityThreshold(b *Bundle) float64 {
	if v, ok := b.Config[qualityKey]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return defaultQualityGate
}

// configValue returns one snapshot value, redaction-safe.
func configValue(b *Bundle, key string) string {
	if v, ok := b.Config[key]; ok && v != "" {
		return esc(v)
	}
	return ""
}

// hasWarningCode reports whether a run warning with the given code is
// recorded (used to avoid double-reporting an issue the run already warned
// about).
func (b *Bundle) hasWarningCode(code string) bool {
	for _, w := range b.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}
