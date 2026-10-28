package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Styles shared by every screen. Adaptive colors keep the TUI readable on
// light and dark terminals.
var (
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stFocus = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	stErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	stOk    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stRun   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	stInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	stHead  = lipgloss.NewStyle().Bold(true)
)

// statusStyle picks the style for a run or stage status string.
func statusStyle(status string) lipgloss.Style {
	switch status {
	case "succeeded":
		return stOk
	case "failed":
		return stErr
	case "cancelled":
		return stWarn
	case "running":
		return stRun
	}
	return stDim
}

// spinnerFrames is the spinner animation for in-flight stages.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// View implements tea.Model.
func (m Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		m.width, m.height = 80, 24
	}
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString("\n")

	// Budget the body height: header, status line, key line, and any
	// overlays.
	body := m.height - 3
	if m.help {
		body -= len(wrapWidth(m.renderHelp(), m.width))
	}
	if m.confirm != nil {
		if m.confirm.detail == "" {
			body -= 5
		} else {
			body -= 3 + len(wrapWidth(m.confirm.detail, 42))
		}
	}
	if body < 3 {
		body = 3
	}
	b.WriteString(m.renderBody(body))

	if m.help {
		b.WriteString("\n" + m.renderHelp())
	}
	if m.confirm != nil {
		b.WriteString("\n" + m.renderConfirm())
	}
	b.WriteString("\n" + m.renderStatusLine())
	b.WriteString("\n" + m.renderKeyLine())
	return b.String()
}

// renderHeader shows the app title and the workspace location.
func (m Model) renderHeader() string {
	left := stTitle.Render("local newsroom")
	if m.info.WorkspaceRoot != "" {
		left += stDim.Render("  ·  " + m.info.WorkspaceRoot)
	}
	right := stDim.Render(" ? help ")
	return leftRight(left, right, m.width)
}

// renderBody renders the active screen within the given line budget.
func (m Model) renderBody(height int) string {
	switch m.screen {
	case screenNew:
		return m.renderNew(height)
	case screenRun:
		return m.renderRun(height)
	case screenPublish:
		return m.renderPublish(height)
	default:
		return m.renderList(height)
	}
}

// renderList renders the recent-runs list.
func (m Model) renderList(height int) string {
	if len(m.runs) == 0 {
		msg := "No runs yet in this workspace.\n\n  Press n to start a new run."
		return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, msg)
	}

	lines := make([]string, 0, len(m.runs))
	lines = append(lines, stHead.Render(fmt.Sprintf("Recent runs (%d)", len(m.runs))))
	for i, r := range m.runs {
		icon := "○"
		st := statusStyle(r.Status)
		switch r.Status {
		case "running":
			icon = "●"
		case "succeeded":
			icon = "✓"
		case "failed":
			icon = "✗"
		case "cancelled":
			icon = "↺"
		}
		row := st.Render(icon) + " " + st.Render(padStatus(r.Status, 9)) + " " +
			truncate(r.ID, 18) + "  "
		row += truncate(r.Topic, max(8, m.width-60))
		if r.Profile != "" {
			row += stDim.Render("  " + r.Profile)
		}
		row += stDim.Render("  " + ago(r.UpdatedAt, time.Now()))
		if r.SourceMode != "" && r.SourceMode != "discovered" {
			row += stDim.Render("  [" + r.SourceMode + "]")
		}
		if r.Warnings > 0 || r.Errors > 0 {
			row += stWarn.Render(fmt.Sprintf("  !%d", r.Warnings+r.Errors))
		}
		if i == m.runSel {
			lines = append(lines, stFocus.Render("▸")+" "+row)
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return clipLines(lines, m.width, height)
}

// renderNew renders the new-run wizard.
func (m Model) renderNew(height int) string {
	var b strings.Builder

	// Endpoint status (never shows secrets).
	llmLine := "LLM:     not configured — runs will fail at research/verify/write"
	if m.info.LLMConfigured {
		llmLine = fmt.Sprintf("LLM:     %s (model %s)", m.info.LLMBaseURL, m.info.LLMModel)
	}
	searchLine := "Search:  not configured — automatic sources unavailable"
	if m.info.SearchConfigured {
		searchLine = fmt.Sprintf("Search:  %s", m.info.SearchBaseURL)
	}
	b.WriteString(stDim.Render(llmLine) + "\n")
	b.WriteString(stDim.Render(searchLine) + "\n\n")

	w := m.wiz
	labels := wizardFields()

	// 1. Topic
	focus := labels[0] == "topic" && w.field == 0
	topicNote := "the editorial topic"
	if w.recipe.current().label != "" && w.recipe.current().label != "(none)" {
		topicNote = "the editorial topic (a selected recipe may derive it)"
	}
	b.WriteString(m.renderFieldLine(0, "topic", stFocus.Render("> ")+" "+truncate(w.topic.buf, 60), topicNote, focus, &b))
	b.WriteString("\n")

	// 2. Recipe
	b.WriteString(m.renderFieldLine(1, "recipe", w.recipe.current().label, "a declarative format preset (v0.6)", w.field == 1, &b))
	if w.field == 1 {
		b.WriteString("\n" + stDim.Render("   "+strings.Join(optionLabels(w.recipe), "  ")))
	}
	b.WriteString("\n")

	// 3. Recipe inputs
	recipeLabel := w.recipe.current().label
	inputsNote := "not used without a recipe"
	if recipeLabel != "" && recipeLabel != "(none)" {
		inputsNote = "name=value pairs for the recipe (space separated)"
	}
	b.WriteString(m.renderFieldLine(2, "inputs", stFocus.Render("> ")+" "+truncate(w.recipeInputs.buf, 50), inputsNote, w.field == 2, &b))
	b.WriteString("\n")

	// 4. Profile
	b.WriteString(m.renderFieldLine(3, "profile", w.profile.current().label, "(none) uses no profile", w.field == 3, &b))
	if w.field == 3 {
		b.WriteString("\n" + stDim.Render("   "+strings.Join(optionLabels(w.profile), "  ")))
	}
	b.WriteString("\n")

	// 5. Format
	b.WriteString(m.renderFieldLine(4, "format", w.format.current().label, "article, thread, or hybrid", w.field == 4, &b))
	if w.field == 4 {
		b.WriteString("\n" + stDim.Render("   "+strings.Join(optionLabels(w.format), "  ")))
	}
	b.WriteString("\n")

	// 6. Source mode
	b.WriteString(m.renderFieldLine(5, "source mode", w.mode.current().label, "how sources are obtained", w.field == 5, &b))
	if w.field == 5 {
		b.WriteString("\n" + stDim.Render("   "+strings.Join(optionLabels(w.mode), "  ")))
	}
	b.WriteString("\n")

	// 7. URLs
	urlNote := "one or more, space separated"
	if w.mode.current().label == "auto (search)" {
		urlNote = "not used in auto mode"
	}
	b.WriteString(m.renderFieldLine(6, "URLs", stFocus.Render("> ")+" "+truncate(w.urls.buf, 60), urlNote, w.field == 6, &b))
	b.WriteString("\n")

	// 8. Follow-up
	fu := "off"
	if w.followUp {
		fu = "on"
	}
	if !w.followUpOn {
		fu += " (needs search)"
	}
	b.WriteString(m.renderFieldLine(7, "follow-up", fu, "y/n toggle: research unresolved claims", w.field == 7, &b))

	if w.err != "" {
		b.WriteString("\n\n" + stErr.Render("  "+w.err))
	}
	return clipLines(strings.Split(b.String(), "\n"), m.width, height)
}

// renderFieldLine renders one labeled wizard field.
func (m Model) renderFieldLine(idx int, name, value, note string, focus bool, b *strings.Builder) string {
	label := name
	if focus {
		label = stFocus.Render("> " + label)
	} else {
		label = "  " + label
	}
	line := fmt.Sprintf("%-13s %s", label, value)
	if note != "" {
		line += stDim.Render("  " + note)
	}
	return line
}

// renderRun renders the run view (in-flight or terminal).
func (m Model) renderRun(height int) string {
	var b strings.Builder

	d := m.detail
	head := stTitle.Render(truncate(d.ID, max(10, m.width/2))) + " " +
		statusStyle(d.Status).Render("["+d.Status+"]")
	b.WriteString(head + "\n")
	if d.Topic != "" {
		for _, l := range wrapWidth(truncate(d.Topic, m.width-2), m.width-2) {
			b.WriteString("  " + stDim.Render(l) + "\n")
		}
	}
	if d.Status == "" || d.Status == "pending" {
		b.WriteString(stDim.Render("  starting…\n"))
	}

	// Stages.
	if len(d.Stages) > 0 {
		b.WriteString("\n" + stHead.Render("  stages") + stDim.Render(fmt.Sprintf("   (%d of %d done)", countDone(d.Stages), len(d.Stages))) + "\n")
		now := time.Now()
		for i, s := range d.Stages {
			b.WriteString(m.renderStageLine(i, s, now) + "\n")
		}
	}

	// Warnings and errors (bounded by default, full with verbose).
	if len(d.Warnings) > 0 || len(d.Errors) > 0 {
		b.WriteString("\n")
		if len(d.Warnings) > 0 {
			b.WriteString(stWarn.Render(fmt.Sprintf("  warnings (%d)", len(d.Warnings))) + "\n")
			for _, w := range lastN(d.Warnings, 3) {
				b.WriteString("    ⚠ " + m.renderLong(w, 0) + "\n")
			}
		}
		if len(d.Errors) > 0 {
			b.WriteString(stErr.Render(fmt.Sprintf("  errors (%d)", len(d.Errors))) + "\n")
			for _, e := range lastN(d.Errors, 3) {
				b.WriteString("    ✗ " + m.renderLong(e, 0) + "\n")
			}
		}
	}

	// Result (terminal runs).
	if isTerminal(d.Status) {
		b.WriteString("\n")
		if d.Reason != "" {
			b.WriteString(stErr.Render("  result: ") + m.renderLong("  "+d.Reason, 0) + "\n")
		} else if d.FinalCheck == "pass" {
			b.WriteString(stOk.Render("  result: passed final check") + "\n")
		}
		if summary := m.renderSummary(); summary != "" {
			b.WriteString(stDim.Render(summary) + "\n")
		}
	}

	// Artifacts.
	if len(d.Artifacts) > 0 {
		b.WriteString("\n" + stHead.Render("  artifacts") + stDim.Render(fmt.Sprintf(" (%d)", len(d.Artifacts))) + "\n")
		for _, a := range d.Artifacts {
			b.WriteString("    • " + a.Path + " " + stDim.Render(humanSize(a.Size)) + "\n")
		}
		b.WriteString(stDim.Render("    o opens an artifact in $EDITOR") + "\n")
	}

	return clipLines(strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), m.width, height)
}

// renderPublish renders the controlled-publication review screen (v0.6,
// #122): the gate verdict, the preset, and one block per destination plan
// with its rehearsal, approval, receipts, and remediation.
func (m Model) renderPublish(height int) string {
	var b strings.Builder

	head := stTitle.Render(truncate(m.pubRun, max(10, m.width/2)))
	if st := m.pubState; st != nil && st.RunStatus != "" {
		head += " " + statusStyle(st.RunStatus).Render("["+st.RunStatus+"]")
	}
	b.WriteString("publication — " + head + "\n")

	if m.pubState == nil {
		if m.pubErr != "" {
			b.WriteString("  " + stErr.Render(truncate(m.pubErr, max(10, m.width-4))) + "\n")
		} else {
			b.WriteString("  " + spinnerFrames[m.frame%len(spinnerFrames)] + " " + stInfo.Render("loading the publication plan…") + "\n")
		}
		return clipLines(strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), m.width, height)
	}
	st := m.pubState

	// An intent that failed (a refused approval, a rejected resume) is
	// always visible, even over the loaded state.
	if m.pubErr != "" {
		b.WriteString("  " + stErr.Render("⚠ "+truncate(m.pubErr, max(10, m.width-6))) + "\n")
	}

	// Gate verdict.
	if st.Ready {
		b.WriteString(stOk.Render("  ready for publication") + "\n")
	} else {
		b.WriteString(stErr.Render("  not ready for publication") + "\n")
		if st.ReadyReason != "" {
			for _, l := range wrapWidth("      "+st.ReadyReason, max(10, m.width-6)) {
				b.WriteString(stDim.Render(l) + "\n")
			}
		}
	}

	// Preset.
	if st.PresetName != "" {
		line := stHead.Render("  preset ") + st.PresetName
		if st.PresetHash != "" {
			line += stDim.Render(" (" + shortDigest(st.PresetHash) + ")")
		}
		b.WriteString(line + "\n")
	} else if st.NoPresetReason != "" {
		b.WriteString(stWarn.Render("  preset: none — ") + stDim.Render(truncate(st.NoPresetReason, max(10, m.width-20))) + "\n")
	}

	// Final check.
	if st.FinalCheck != "" {
		line := stHead.Render("  final check ") + finalCheckStyle(st.FinalCheck).Render(st.FinalCheck)
		if st.Findings > 0 {
			line += stDim.Render(fmt.Sprintf(" (%d findings)", st.Findings))
		}
		b.WriteString(line + "\n")
	}

	// A cycle interrupted mid-flight is always surfaced.
	if st.Interrupted {
		b.WriteString(stWarn.Render("  ⚠ the last publication cycle was interrupted — the state is resumable (r)") + "\n")
	}

	// Plans.
	if len(st.Plans) == 0 {
		b.WriteString(stDim.Render("  no plans for this run yet — press a to approve and publish") + "\n")
	} else {
		b.WriteString("\n")
		for i := range st.Plans {
			b.WriteString(m.renderPlanBlock(i, &st.Plans[i], st) + "\n")
		}
	}

	return clipLines(strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), m.width, height)
}

// finalCheckStyle picks the style for a final-check verdict.
func finalCheckStyle(status string) lipgloss.Style {
	switch status {
	case "pass":
		return stOk
	case "fail":
		return stErr
	case "review":
		return stWarn
	}
	return stDim
}

// shortDigest abbreviates a digest for display.
func shortDigest(d string) string {
	if len(d) > 18 {
		return d[:18] + "…"
	}
	return d
}

// renderPlanBlock renders one destination plan block (selected or not).
func (m Model) renderPlanBlock(i int, p *PublicationPlan, st *PublicationState) string {
	var b strings.Builder

	label := p.Key
	if label == "" {
		label = p.Destination
	}
	if label == "" {
		label = "plan"
	}
	head := stHead.Render(label)
	if p.Destination != "" && p.Destination != label {
		head += stDim.Render(" → " + truncate(p.Destination, max(10, m.width-24)))
	}
	if p.PlanID != "" {
		head += " " + statusStyle(p.Status).Render("["+p.Status+"]")
	} else {
		head += " " + stDim.Render("[no plan]")
	}
	if i == m.pubSel {
		b.WriteString("  " + stFocus.Render("▸ ") + head + "\n")
	} else {
		b.WriteString("    • " + head + "\n")
	}

	// Provenance + kind.
	if p.Kind != "" {
		b.WriteString(stDim.Render("      kind   "+p.Kind) + "\n")
	}

	// Facts (branch, path, post count, ...).
	if facts := planFactsLine(p.Facts, m.width); facts != "" {
		b.WriteString("      " + stDim.Render(facts) + "\n")
	}

	// Baseline content + digest.
	if p.ArtifactPath != "" {
		line := "      " + stHead.Render("content  ") + truncate(p.ArtifactPath, max(10, m.width-28))
		if p.ArtifactSize > 0 {
			line += stDim.Render("  " + humanSize(p.ArtifactSize))
		}
		if p.ArtifactVersion != "" {
			line += stDim.Render("  " + p.ArtifactVersion)
		}
		if p.ContentStale {
			line += stWarn.Render("  ⚠ changed since baseline")
		}
		if p.ContentUnavailable {
			line += stErr.Render("  ⚠ baseline file missing")
		}
		b.WriteString(line + "\n")
	}
	if p.ArtifactDigest != "" {
		b.WriteString("      " + stHead.Render("digest   ") + stDim.Render(shortDigest(p.ArtifactDigest)) + "\n")
	}

	// Targets: state, receipt, or failure.
	for _, t := range p.Targets {
		b.WriteString(m.renderPubTarget(t) + "\n")
	}

	// Rehearsal.
	if t := m.previewLine(p); t != "" {
		b.WriteString(t + "\n")
	}

	// Approval.
	if p.ApprovalBy != "" {
		line := "      " + stHead.Render("approval ")
		style := finalCheckStyle(p.ApprovalStatus)
		if p.ApprovalStatus == "" {
			style = stDim
		}
		line += style.Render(truncate(p.ApprovalBy, max(8, m.width-34)))
		if p.ApprovalMethod != "" {
			line += stDim.Render(" (" + p.ApprovalMethod)
			if p.ApprovalStatus != "" {
				line += ", " + p.ApprovalStatus
			}
			line += ")"
		}
		if !p.ApprovalEffective {
			line += stWarn.Render(" ⚠ does not cover the current content")
		}
		b.WriteString(line + "\n")
	} else {
		b.WriteString("      " + stHead.Render("approval ") + stDim.Render("none") + "\n")
	}

	// Terminal reason.
	if p.Reason != "" {
		for _, l := range wrapWidth("      reason  "+p.Reason, max(10, m.width-12)) {
			b.WriteString(stDim.Render(l) + "\n")
		}
	}

	// Plan-level diagnostics.
	for _, w := range lastN(p.Warnings, 2) {
		b.WriteString("      " + stWarn.Render(truncate("⚠ "+w, max(10, m.width-8))) + "\n")
	}
	for _, e := range lastN(p.Errors, 2) {
		b.WriteString("      " + stErr.Render(truncate("✗ "+e, max(10, m.width-8))) + "\n")
	}

	// Remediation after a failed / interrupted cycle.
	if len(p.Remediation) > 0 {
		limit := 3
		if m.verbose {
			limit = len(p.Remediation)
		}
		if limit > len(p.Remediation) {
			limit = len(p.Remediation)
		}
		if len(p.Remediation) > limit {
			b.WriteString(stDim.Render(fmt.Sprintf("      fix   (%d steps)", len(p.Remediation))) + "\n")
		} else {
			b.WriteString(stHead.Render("      fix") + "\n")
		}
		for _, r := range p.Remediation[:limit] {
			line := r.Action
			if r.Detail != "" {
				line += " — " + r.Detail
			}
			for _, l := range wrapWidth("        → "+line, max(10, m.width-12)) {
				b.WriteString(stWarn.Render(l) + "\n")
			}
		}
		if !m.verbose && len(p.Remediation) > limit {
			b.WriteString(stDim.Render("        (v shows all)") + "\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderPubTarget renders one plan target with its latest state.
func (m Model) renderPubTarget(t PublicationTarget) string {
	name := t.Name
	if name == "" {
		name = t.ID
	}
	line := "      " + stHead.Render(padRight(name, 10))
	switch t.Status {
	case "publishing":
		line += stRun.Render(spinnerFrames[m.frame%len(spinnerFrames)] + " in flight")
	case "succeeded":
		line += stOk.Render("✓ ")
		if t.ReceiptURI != "" {
			line += stOk.Render(truncate(t.ReceiptURI, max(8, m.width-28)))
			if m.verbose && t.ReceiptRecordID != "" {
				line += stDim.Render("  (" + t.ReceiptRecordID + ")")
			}
		} else {
			line += stOk.Render("published")
		}
	case "failed":
		line += stErr.Render("✗ ")
		if t.Error != "" {
			line += stErr.Render(truncate(t.Error, max(8, m.width-28)))
		} else {
			line += stErr.Render("failed")
		}
	case "skipped":
		line += stDim.Render("· skipped")
		if t.SkipReason != "" {
			line += stDim.Render(" — " + truncate(t.SkipReason, max(8, m.width-34)))
		}
	case "cancelled":
		line += stWarn.Render("↺ cancelled")
	case "planned", "approved":
		line += statusStyle(t.Status).Render(t.Status)
	default:
		line += stDim.Render(t.Status)
	}
	return line
}

// previewLine renders the plan's latest rehearsal ("" when none is
// available; the verbose mode adds the publisher's metadata).
func (m Model) previewLine(p *PublicationPlan) string {
	if len(p.Targets) == 0 {
		return ""
	}
	t := p.Targets[0]
	if t.PreviewOK == nil {
		return "      " + stHead.Render("preview  ") + stDim.Render("no rehearsal available")
	}
	line := "      " + stHead.Render("preview  ")
	if *t.PreviewOK {
		line += stOk.Render("ok")
	} else {
		line += stErr.Render("blocked")
	}
	if t.PreviewDetails != "" {
		line += stDim.Render(" — " + truncate(t.PreviewDetails, max(8, m.width-28)))
	}
	out := line + "\n"
	limit := 2
	if m.verbose {
		limit = len(t.PreviewIssues)
	}
	if limit > len(t.PreviewIssues) {
		limit = len(t.PreviewIssues)
	}
	if len(t.PreviewIssues) > 0 {
		for _, issue := range t.PreviewIssues[:limit] {
			for _, l := range wrapWidth("            "+issue, max(10, m.width-14)) {
				out += stWarn.Render(l) + "\n"
			}
		}
		if !m.verbose && len(t.PreviewIssues) > limit {
			out += stDim.Render("            (v shows all)") + "\n"
		}
	}
	if m.verbose {
		for k, v := range t.PreviewMetadata {
			out += stDim.Render(fmt.Sprintf("            %s %s\n", k, v))
		}
	}
	return strings.TrimRight(out, "\n")
}

// planFactsLine renders up to three destination facts, joined.
func planFactsLine(facts map[string]string, width int) string {
	if len(facts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if len(parts) == 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(facts)-3))
			break
		}
		parts = append(parts, k+" "+facts[k])
	}
	return truncate(strings.Join(parts, "   ·   "), max(10, width-12))
}
func (m Model) renderStageLine(i int, s StageInfo, now time.Time) string {
	icon := "○"
	st := statusStyle(s.Status)
	switch s.Status {
	case "succeeded":
		icon = "✓"
	case "failed":
		icon = "✗"
	case "cancelled":
		icon = "↺"
	case "skipped":
		icon = "·"
	case "running":
		icon = spinnerFrames[m.frame%len(spinnerFrames)]
	}
	name := padRight(s.Name, 14)
	line := st.Render(icon) + " " + name
	switch {
	case s.Status == "skipped" && s.SkipReason != "":
		line += stDim.Render(truncate(s.SkipReason, max(10, m.width-30)))
	case s.Status == "running" && !s.StartedAt.IsZero():
		line += stDim.Render(fmt.Sprintf("%ds elapsed", int(now.Sub(s.StartedAt).Seconds())))
	case s.Status == "failed" && s.Error != "":
		line += stErr.Render(truncate(s.Error, max(10, m.width-30)))
	}
	if s.Attempts > 1 {
		line += stDim.Render(fmt.Sprintf("  ×%d", s.Attempts))
	}
	if i == m.stageSel {
		return stFocus.Render("▸ ") + line
	}
	return "   " + line
}

// renderSummary renders the one-line run summary.
func (m Model) renderSummary() string {
	d := m.detail
	parts := make([]string, 0, 5)
	if d.SourcesAttempted > 0 || d.SourcesFetched > 0 {
		parts = append(parts, fmt.Sprintf("sources %d/%d", d.SourcesFetched, d.SourcesAttempted))
	}
	if d.Documents > 0 {
		parts = append(parts, fmt.Sprintf("%d documents", d.Documents))
	}
	if d.ClaimsTotal > 0 {
		p := fmt.Sprintf("claims %d (%d supported)", d.ClaimsTotal, d.ClaimsSupported)
		if d.ClaimsUnresolved > 0 {
			p += fmt.Sprintf(", %d unresolved", d.ClaimsUnresolved)
		}
		parts = append(parts, p)
	}
	if d.QualityScore != nil {
		parts = append(parts, fmt.Sprintf("quality %s", formatScore(*d.QualityScore)))
	}
	if d.FinalCheck != "" {
		p := d.FinalCheck
		if d.Findings > 0 {
			p = fmt.Sprintf("%s (%d findings)", d.FinalCheck, d.Findings)
		}
		parts = append(parts, "final check "+p)
	}
	if d.PublicationStatus != "" {
		p := "publication " + d.PublicationStatus
		if d.PublicationTargets > 0 {
			p = fmt.Sprintf("%s (%d/%d targets ok)", p, d.PublicationSucceeded, d.PublicationTargets)
		}
		if d.PublicationFailed > 0 {
			p = fmt.Sprintf("%s, %d failed", p, d.PublicationFailed)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  summary: " + strings.Join(parts, " · ")
}

// renderLong renders a possibly long line: truncated by default, wrapped
// when verbose.
func (m Model) renderLong(line string, indent int) string {
	if !m.verbose {
		return truncate(indentString(line, indent), max(10, m.width-indent-2))
	}
	wrapped := wrapWidth(indentString(line, indent), max(10, m.width-indent-2))
	return strings.Join(wrapped, "\n")
}

// renderStatusLine renders the transient status / error line.
func (m Model) renderStatusLine() string {
	if m.err != "" {
		return truncate(stErr.Render("  "+m.err), m.width)
	}
	if m.status != "" {
		return truncate(stInfo.Render("  "+m.status), m.width)
	}
	return ""
}

// renderKeyLine renders the per-screen key hints.
func (m Model) renderKeyLine() string {
	var hints []string
	switch m.screen {
	case screenList:
		hints = []string{"j/k or ↑/↓ select", "enter detail", "n new run", "r resume", "q quit"}
	case screenNew:
		hints = []string{"tab/↑/↓ fields", "←/→ options", "enter next/start", "esc back"}
	case screenRun:
		if m.executing {
			hints = []string{"c cancel", "v verbose", "q quit"}
		} else {
			hints = []string{"j/k stage", "t retry stage", "r resume", "p publish", "o open artifact", "v verbose", "q back"}
		}
	case screenPublish:
		switch m.pubPhase {
		case pubRunning:
			hints = []string{"c cancel", "q quit", "esc back"}
		case pubConfirm:
			hints = []string{"y confirm", "n/esc dismiss"}
		default: // pubReview / pubDone
			hints = []string{"a approve & publish", "r resume", "j/k plan", "q quit", "esc back"}
		}
	}
	if len(hints) == 0 {
		return ""
	}
	return leftRight(stDim.Render(strings.Join(hints, "   ")), stDim.Render(" ? help "), m.width)
}

// renderHelp renders the extended help overlay.
func (m Model) renderHelp() string {
	lines := []string{
		"Keyboard",
		"  q / ctrl+c   quit (asks if a run is in progress)",
		"  ?            toggle this help",
		"  ctrl+l       redraw",
		"",
		"Recent runs",
		"  j/k, ↑/↓     select run",
		"  enter        run detail / live view",
		"  n            new run",
		"  r            resume the selected run",
		"",
		"New run",
		"  tab, ↑/↓     move between fields",
		"  ←/→          choose an option (skip disabled)",
		"  enter        next field; on the last field: start",
		"  esc          back to the run list",
		"  y/n          toggle follow-up research",
		"",
		"Run view",
		"  j/k, ↑/↓     select a stage",
		"  c            cancel the running run (asks first)",
		"  t            retry the selected stage (invalidates downstream)",
		"  r            resume after a failure",
		"  p            open the publication review (terminal runs)",
		"  o            open an artifact in $EDITOR (cycles)",
		"  v            toggle verbose (full warning/error text)",
		"  q / esc      back to the run list",
		"",
		"Publication (v0.6)",
		"  a            approve & publish the plans (asks first)",
		"  r            resume a failed / interrupted cycle (asks first)",
		"  c            cancel the in-flight cycle (asks first)",
		"  j/k, ↑/↓     select a plan",
		"  v            toggle verbose (full preview / receipt text)",
		"  esc          back to the run view",
		"  q / ctrl+c   quit the app (asks first while publishing)",
	}
	return strings.Join(lines, "\n")
}

// renderConfirm renders the modal prompt.
func (m Model) renderConfirm() string {
	c := m.confirm
	lines := []string{
		"┌────────────────────────────────────────────────┐",
		fmt.Sprintf("│  %s", truncate(c.prompt, 42)),
	}
	if c.detail != "" {
		for _, l := range wrapWidth(c.detail, 42) {
			lines = append(lines, fmt.Sprintf("│  %s", l))
		}
	}
	lines = append(lines,
		"│                                                │",
		"│   [y]es        [n]o (esc)                      │",
		"└────────────────────────────────────────────────┘",
	)
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Text helpers
// ---------------------------------------------------------------------------

// truncate shortens s to fit within maxW display cells ("" when maxW <= 0).
// A cut string ends with an ellipsis taking the last cell.
func truncate(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	if maxW < 4 {
		return strings.Repeat("…", maxW)
	}
	limit := maxW - 1 // reserve the last cell for the ellipsis
	var b strings.Builder
	width := 0
	for _, r := range s {
		cw := lipgloss.Width(string(r))
		if width+cw > limit {
			return b.String() + "…"
		}
		b.WriteRune(r)
		width += cw
	}
	return s // unreachable: lipgloss.Width(s) > maxW
}

// leftRight places left and right on one line within width.
func leftRight(left, right string, width int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	if lw+rw+1 >= width {
		// Not enough room for both: keep the left content and truncate it to
		// the terminal width (never let a line overflow the screen).
		return truncate(left, width)
	}
	return left + strings.Repeat(" ", width-lw-rw) + right
}

// clipLines trims a block to fit within width and height lines.
func clipLines(lines []string, width, height int) string {
	if height <= 0 {
		height = 1
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = truncate(l, width)
	}
	return strings.Join(out, "\n")
}

// wrapWidth wraps s into lines of at most width display cells.
func wrapWidth(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	var lines []string
	var cur strings.Builder
	w := 0
	for _, r := range s {
		cw := lipgloss.Width(string(r))
		if cw == 0 {
			continue
		}
		if w+cw > width {
			lines = append(lines, strings.TrimSpace(cur.String()))
			cur.Reset()
			w = 0
			cw = lipgloss.Width(string(r))
		}
		cur.WriteRune(r)
		w += cw
	}
	lines = append(lines, strings.TrimSpace(cur.String()))
	return lines
}

// lastN returns the last n entries of in.
func lastN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}

// countDone counts succeeded+skipped stages.
func countDone(stages []StageInfo) int {
	n := 0
	for _, s := range stages {
		if s.Status == "succeeded" || s.Status == "skipped" {
			n++
		}
	}
	return n
}

// padStatus right-pads a status to width cells for column alignment.
func padStatus(s string, width int) string {
	if lipgloss.Width(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-lipgloss.Width(s))
}

// padRight right-pads s to width cells.
func padRight(s string, width int) string {
	if lipgloss.Width(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-lipgloss.Width(s))
}

// optionLabels returns the option labels, marking disabled ones.
func optionLabels(s selector) []string {
	out := make([]string, len(s.opts))
	for i, o := range s.opts {
		if i == s.sel {
			out[i] = stFocus.Render(o.label)
		} else if !o.enabled {
			out[i] = stDim.Render(o.label + " (off)")
		} else {
			out[i] = o.label
		}
	}
	return out
}

// ago renders a compact relative time.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return t.Format("15:04")
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2 15:04")
	}
}

// humanSize renders a byte count compactly.
func humanSize(n int64) string {
	switch {
	case n < 0:
		return ""
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// formatScore renders a quality score (0-100) without trailing zeros.
func formatScore(f float64) string {
	if f == float64(int(f)) {
		return fmt.Sprintf("%d", int(f))
	}
	return fmt.Sprintf("%.1f", f)
}

// indentString prefixes every line of s with indent spaces.
func indentString(s string, indent int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Repeat(" ", indent) + l
	}
	return strings.Join(lines, "\n")
}
