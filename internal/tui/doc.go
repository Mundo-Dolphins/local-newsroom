// Package tui implements the keyboard-first terminal interface of
// local-newsroom (v0.5).
//
// # Architecture boundary
//
// This package is a pure presentation layer over the persisted
// run/workflow services. It:
//
//   - renders runs, stages, warnings, errors, and artifacts as plain data
//     handed to it by the application layer through the Service interface;
//   - translates keyboard input and terminal events into user actions
//     (create run, resume, retry stage, cancel, open artifact);
//   - contains no pipeline business logic: no stage rules, no LLM calls,
//     no search, no persistence of its own. Every run action it performs
//     is a call into the Service, whose implementation (in the application
//     layer) wires the existing orchestrator, resume, workspace, and
//     configuration services — the same wiring the `newsroom run` command
//     uses, so the CLI and the TUI share one pipeline path.
//
// # Remote behavior
//
// The TUI runs locally on the user's machine and talks to the configured
// remote endpoints (oMLX on the Mac Studio, SearXNG on the Raspberry Pi)
// only through the services the Service implementation constructs from the
// resolved configuration. It starts no daemon of its own: a run executes
// inside the TUI process and the terminal shows its progress by observing
// the run manifest the runner persists after every state change.
//
// # Display safety
//
// Credentials never cross the Service boundary, and free-text values shown
// to the user (topics, warnings, error summaries) come from the already
// redacted run manifest. Raw LLM responses (dossiers, verification detail)
// are never rendered: the default view shows stage status, bounded
// warning/error lines, and artifact paths only.
//
// # Implementation
//
// The terminal plumbing is Bubble Tea (raw mode, alternate screen, resize
// via WindowSizeMsg, key decoding) with Lip Gloss styling. The UI itself is
// the Model state machine in model.go and the rendering in view.go: a plain
// Go struct whose Update is a total function of (model, message), so the
// complete state/update behavior is unit-testable without a terminal.
//
// The service contract and its data types live in service.go.

package tui
