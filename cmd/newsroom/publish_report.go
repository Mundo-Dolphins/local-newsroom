package main

// JSON output for newsroom publish: the same projections the text mode
// shows, in a stable machine-readable form for automation. Every value is
// display material already sanitized/redacted by the engine at persistence,
// so no new secret surface is introduced.

import (
	"encoding/json"
	"fmt"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// pubRun identifies the run a publication belongs to.
type pubRun struct {
	ID        string `json:"id"`
	Topic     string `json:"topic"`
	Status    string `json:"status"`
	Workspace string `json:"workspace,omitempty"`
}

func pubRunOf(m *run.Manifest) pubRun {
	return pubRun{ID: string(m.ID), Topic: m.Topic, Status: string(m.Status), Workspace: m.Workspace.Name}
}

// pubReady is the pre-publication gate determination.
type pubReady struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

// pubPreset is the resolved publication preset.
type pubPreset struct {
	Name       string `json:"name"`
	Hash       string `json:"hash,omitempty"`
	BundleRoot string `json:"bundle_root,omitempty"`
}

// pubArtifact is the plan's content identity.
type pubArtifact struct {
	ID     string `json:"id,omitempty"`
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// pubTarget is one target's state, receipt, and rehearsal.
type pubTarget struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	Provider        string            `json:"provider"`
	Destination     string            `json:"destination,omitempty"`
	Status          string            `json:"status"`
	Attempts        int               `json:"attempts"`
	Receipt         json.RawMessage   `json:"receipt,omitempty"`
	Error           string            `json:"error,omitempty"`
	PreviewOK       *bool             `json:"preview_ok,omitempty"`
	Preview         string            `json:"preview,omitempty"`
	PreviewIssues   []string          `json:"preview_issues,omitempty"`
	PreviewMetadata map[string]string `json:"preview_metadata,omitempty"`
}

// pubApproval is the latest approval recorded on the plan.
type pubApproval struct {
	ID             string `json:"id"`
	ApprovedBy     string `json:"approved_by"`
	Method         string `json:"method"`
	Status         string `json:"status"`
	ArtifactDigest string `json:"artifact_digest"`
}

// pubRemediation is one manual step for a human.
type pubRemediation struct {
	Kind     string `json:"kind"`
	TargetID string `json:"target_id,omitempty"`
	Action   string `json:"action"`
	Detail   string `json:"detail,omitempty"`
	URI      string `json:"uri,omitempty"`
	RecordID string `json:"record_id,omitempty"`
}

// pubPlan is one publication plan in the report.
type pubPlan struct {
	ID             string           `json:"id"`
	Destination    string           `json:"destination,omitempty"`
	Status         string           `json:"status"`
	Artifact       pubArtifact      `json:"artifact"`
	Interrupted    bool             `json:"interrupted,omitempty"`
	TerminalReason string           `json:"terminal_reason,omitempty"`
	Approval       *pubApproval     `json:"approval,omitempty"`
	Targets        []pubTarget      `json:"targets"`
	Remediation    []pubRemediation `json:"remediation,omitempty"`
	Warnings       []string         `json:"warnings,omitempty"`
	Errors         []string         `json:"errors,omitempty"`
}

// pubReport is the full JSON document one publish invocation produces.
type pubReport struct {
	Run       pubRun     `json:"run"`
	Ready     *pubReady  `json:"ready,omitempty"`
	Preset    *pubPreset `json:"preset,omitempty"`
	Mode      string     `json:"mode"`
	Plans     []pubPlan  `json:"plans,omitempty"`
	Published bool       `json:"published"`
	ExitCode  int        `json:"exit_code"`
	Error     string     `json:"error,omitempty"`
}

// pubReportPlan projects one plan (with its optional destination and cycle
// summary) into the report form.
func pubReportPlan(plan *publication.Plan, dest *presets.Destination, sum *publish.Summary) pubPlan {
	pi := pubPlan{
		ID:             string(plan.ID),
		Status:         string(plan.Status),
		Artifact:       pubArtifact{ID: plan.Artifact.ID, Path: plan.Artifact.Path, Digest: string(plan.Artifact.Digest), Size: plan.Artifact.Size},
		TerminalReason: plan.TerminalReason,
	}
	if dest != nil {
		pi.Destination = dest.Key
	}
	if len(plan.Approvals) > 0 {
		a := plan.Approvals[len(plan.Approvals)-1]
		pi.Approval = &pubApproval{
			ID:             string(a.ID),
			ApprovedBy:     a.ApprovedBy,
			Method:         string(a.Method),
			Status:         string(a.Status),
			ArtifactDigest: string(a.ArtifactDigest),
		}
	}
	for _, w := range plan.Warnings {
		pi.Warnings = append(pi.Warnings, w.Message)
	}
	for _, e := range plan.Errors {
		pi.Errors = append(pi.Errors, e.Message)
	}
	for _, t := range plan.Targets {
		pt := pubTarget{
			ID:          string(t.ID),
			Name:        t.Name,
			Type:        string(t.Type),
			Provider:    t.Provider,
			Destination: t.Destination,
			Status:      string(t.Status),
			Attempts:    len(t.Attempts),
		}
		if t.Receipt != nil {
			if raw, err := json.Marshal(t.Receipt); err == nil {
				pt.Receipt = raw
			}
		}
		if t.Error != nil {
			pt.Error = t.Error.Message
		}
		pi.Targets = append(pi.Targets, pt)
	}
	if sum != nil {
		pi.Interrupted = sum.Interrupted
		for i, t := range sum.Targets {
			if i >= len(pi.Targets) {
				break
			}
			// The summary carries the post-cycle target state; the plan
			// document may predate it, so its status wins the display.
			pi.Targets[i].Status = string(t.Status)
			pi.Targets[i].Attempts = t.Attempts
			if t.Error != nil && t.Error.Message != "" {
				pi.Targets[i].Error = t.Error.Message
			}
			if t.Receipt != nil {
				if raw, err := json.Marshal(t.Receipt); err == nil {
					pi.Targets[i].Receipt = raw
				}
			}
			if t.Preview != nil {
				ok := t.Preview.OK
				pi.Targets[i].PreviewOK = &ok
				pi.Targets[i].Preview = t.Preview.Details
				pi.Targets[i].PreviewMetadata = t.Preview.Metadata
				for _, issue := range t.Preview.Issues {
					pi.Targets[i].PreviewIssues = append(pi.Targets[i].PreviewIssues, issue.Message)
				}
			}
		}
		for _, r := range sum.Remediation {
			pi.Remediation = append(pi.Remediation, pubRemediation{
				Kind:     string(r.Kind),
				TargetID: string(r.TargetID),
				Action:   r.Action,
				Detail:   r.Detail,
				URI:      r.URI,
				RecordID: r.RecordID,
			})
		}
		for _, w := range sum.Warnings {
			pi.Warnings = append(pi.Warnings, w.Message)
		}
	}
	return pi
}

// pubReportPrint marshals the report and prints it (the JSON mode of
// --format). A marshal failure is reported on stderr and does not change
// the process exit code: the report is presentation, not the result.
func pubReportPrint(r pubReport) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		warnf("json: cannot marshal the report: %v", err)
		return
	}
	fmt.Println(string(data))
}
