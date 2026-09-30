// Package orchestrator holds the plumbing that sequences the newsroom's
// internal stages (fetching, researching, verifying).
//
// Nothing is implemented in this scaffold - only a helper used by the CLI.
package orchestrator

// Help is rendered by the root command to document the orchestration flow.
const Help = `orchestrator - sequences the newsroom pipeline

  fetch  -> collect source material
  research -> synthesize findings from the material
  verify -> fact-check the synthesized output

Each stage is not implemented in this scaffold.`
