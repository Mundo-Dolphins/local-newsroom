package run

// Canonical stage artifact file names, relative to the run directory.
//
// Every pipeline stage persists its index/result artifacts under these stable
// paths (the orchestrator writes them; the runner records them on the
// manifest). Consumers that read stage output by path — the orchestrator
// itself (on resume), the summary builder, and the human review bundle —
// share these constants so producer and consumer can never drift apart.
//
// The paths are part of the run-directory layout documented in
// docs/contracts-v0.5.md, not of the manifest JSON document.
const (
	// ArtifactPathSourcesIndex is the source URL selection list written by
	// the discovery stage (every source mode).
	ArtifactPathSourcesIndex = "sources/index.json"

	// ArtifactPathResolvedRecipe is the resolved (merged) recipe document
	// persisted by the discovery stage (v0.6 editorial recipes). Later
	// stages read this file so a resumed run executes the exact recipe the
	// run started with.
	ArtifactPathResolvedRecipe = "recipe/resolved.json"

	// ArtifactPathFetchIndex records the outcome of every attempted URL
	// retrieval (written by the fetch stage).
	ArtifactPathFetchIndex = "fetch/index.json"

	// ArtifactPathDocumentsIndex lists the normalized documents produced by
	// the extract stage.
	ArtifactPathDocumentsIndex = "documents/index.json"

	// ArtifactPathResearchIndex records what the research stage synthesized
	// from (document counts, claim IDs, source IDs).
	ArtifactPathResearchIndex = "research/index.json"

	// ArtifactPathDossier is the research dossier (research stage output).
	ArtifactPathDossier = "research/dossier.json"

	// ArtifactPathVerification is the verification result (verify stage
	// output).
	ArtifactPathVerification = "verify/verification.json"

	// ArtifactPathFollowUp is the follow-up research result (follow_up stage
	// output).
	ArtifactPathFollowUp = "follow_up/result.json"

	// ArtifactPathArtifact is the editorial artifact (write stage output).
	ArtifactPathArtifact = "write/artifact.json"

	// ArtifactPathContentPlan is the content plan computed by the write
	// stage for recipe runs (v0.6 editorial recipes): the expanded
	// blueprint with per-unit claim traceability.
	ArtifactPathContentPlan = "write/content-plan.json"

	// ArtifactPathFinalCheck is the final factual-check result. The file is
	// always persisted when the stage executes; it is referenced on the
	// manifest only when the stage succeeds (a failed gate must not hold
	// artifact references).
	ArtifactPathFinalCheck = "final_check/result.json"

	// ArtifactPathArticle is the rendered article (render stage output).
	ArtifactPathArticle = "output/article.md"

	// ArtifactPathThread is the rendered thread (render stage output).
	ArtifactPathThread = "output/thread.txt"
)

// Publication layout inside the run directory (v0.6 controlled publication).
//
// Publication state is a post-run state, not a pipeline stage: it lives in
// its own directory so the v0.5 stage layout (and every historical manifest
// that references it) stays untouched.
const (
	// PublicationDir is the publication state directory of a run.
	PublicationDir = "publication"

	// PublicationPlansDir holds the versioned publication plan documents,
	// one file per plan: publication/plans/<plan-id>.json. A run keeps its
	// full plan history here; the manifest references only the latest.
	PublicationPlansDir = "publication/plans"

	// PublicationContentDir holds the approved content files that a plan's
	// artifact identity is baselined to (e.g. the canonical NUL-joined
	// thread text): publication/content/<name>.
	PublicationContentDir = "publication/content"
)

// ReviewFileName is the file name of the human review bundle inside the run
// directory. The bundle is a run-directory file (like the manifest), not a
// stage artifact: it is written after the run reaches a terminal state and is
// deliberately never referenced on the manifest, so it can be produced for
// every terminal run (succeeded, failed, and cancelled alike).
const ReviewFileName = "review.md"
