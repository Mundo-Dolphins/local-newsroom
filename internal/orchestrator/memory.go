// This file wires the editorial memory (internal/entitymemory) into the
// research pipeline as the v0.7 memory-hint projection (issue #145).
//
// Design: the memory is read-only context for the planner and the researcher.
// The orchestrator carries a MemoryService (built here, injected via
// Config) whose per-topic projections are consumed in two places:
//
//   - the discovery stage renders the planner hints, records the matched
//     entities in the sources index, and appends the hints to the discovery
//     result so the planner consumes them (without the orchestrator
//     depending on the discovery package's internal types);
//   - the research stage renders the researcher hints, passes them to the
//     research service, and records the matched entities.
//
// The no-bypass invariant is enforced in three layers:
//
//  1. Projection (entitymemory.BuildPromptBlock): values appear only for
//     stale/expired attributes, always labeled memory-only; identity hints
//     carry names/aliases/keys only.
//  2. Prompt framing: every rendered block states that hints are context,
//     never evidence, and the researcher block instructs that unconfirmed
//     values become unverified claims or are dropped.
//  3. Deterministic final-check (finalchecker): a verified claim that
//     matches a memory hint (value or background fact) while lacking
//     live-source evidence is a finding — memory can guide, never verify.
//
// A run over a missing, unreadable, or erroring memory database degrades to
// pre-memory behavior (no hints, no provenance) rather than failing: hints
// are optional context, never a dependency of the research pipeline.
package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/entitymemory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

// MemoryService is the orchestrator's view of the editorial memory: bounded,
// render-ready hint projections for one run topic. Implementations must be
// safe for concurrent use.
//
// Close releases the underlying database handle. It is called by the command
// layer when the run finishes (including on cancellation and failure).
type MemoryService interface {
	// PlannerHints returns the rendered hint block for the planner stage, or
	// "" when no entities matched (or memory is unavailable).
	PlannerHints(ctx context.Context, topic string) (hints string, entityIDs []string, err error)

	// ResearcherHints returns the rendered hint block for the researcher
	// stage, or "" when no entities matched.
	ResearcherHints(ctx context.Context, topic string) (hints string, entityIDs []string, err error)

	// Entities returns the full records (ID, name, attributes, background
	// facts) for the given entity IDs, for the no-bypass check. Unknown IDs
	// are skipped.
	Entities(ctx context.Context, ids []string) ([]entitymemory.Entity, error)

	// Close releases the underlying resources. It is idempotent and safe to
	// call on an already-closed service.
	Close() error
}

// MemoryConfig configures the memory hint projection for a run.
type MemoryConfig struct {
	// Enabled turns the projection on (false = pre-memory behavior).
	Enabled bool
	// DBPath is the memory database path ("" = the memorystore default
	// location; ":memory:" = an empty in-process store).
	DBPath string
	// MaxEntities bounds the entities rendered per stage prompt (0 = the
	// entitymemory default; bounds above the default are clamped).
	MaxEntities int
}

// MemoryFromConfig builds a MemoryConfig from the memory.* settings, using
// the run's config snapshot first (so a resumed run uses the exact
// configuration it started with) and the process Config as fallback.
//
// An invalid or missing value for a memory setting degrades to pre-memory
// behavior rather than failing the run: hints are optional context.
func MemoryFromConfig(cfg *config.Config) MemoryConfig {
	mc := MemoryConfig{}
	if cfg == nil {
		return mc
	}
	if v, _, err := cfg.Resolve(config.SettingMemoryEnabled, config.Override{}); err == nil && v != "" {
		mc.Enabled = v == "true"
	}
	if v, _, err := cfg.Resolve(config.SettingMemoryDBPath, config.Override{}); err == nil && v != "" {
		mc.DBPath = v
	}
	if v, _, err := cfg.Resolve(config.SettingMemoryMaxEntities, config.Override{}); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			mc.MaxEntities = n
		}
	}
	return mc
}

// ---------------------------------------------------------------------------
// storeMemoryService
// ---------------------------------------------------------------------------

// storeMemoryService implements MemoryService over an entitymemory.Store.
type storeMemoryService struct {
	store       *entitymemory.Store
	maxEntities int
	now         time.Time
}

// NewMemoryService opens the memory database at cfg.DBPath and returns a
// MemoryService for it, or (nil, nil) when the memory is disabled or the
// database cannot be opened. A nil result is the signal for pre-memory
// behavior: the pipeline runs exactly as it did before the memory existed.
func NewMemoryService(ctx context.Context, cfg MemoryConfig) (MemoryService, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	backing, err := openMemoryStore(cfg.DBPath)
	if err != nil {
		// A missing or unreadable memory database degrades to pre-memory
		// behavior; the error is surfaced to the caller for a warning, not
		// a failure.
		return nil, err
	}
	store := entitymemory.NewStore(backing)
	if err := store.LoadError(); err != nil {
		return nil, fmt.Errorf("memory store load: %w", err)
	}
	svc := &storeMemoryService{
		store:       store,
		maxEntities: cfg.MaxEntities,
		now:         time.Now().UTC(),
	}
	return svc, nil
}

// openMemoryStore opens the backing v0.7 memory store. An empty path uses
// the memorystore's default location; a path whose directory does not yet
// exist yields a clean error (the store never creates directories outside
// its own tree).
func openMemoryStore(path string) (memorystore.Store, error) {
	if path == "" {
		return memorystore.NewSQLiteStore("", memorystore.DefaultDBName)
	}
	return memorystore.NewSQLiteStoreAt(path)
}

// projectMentions derives the mention set for a topic: the topic itself plus
// a small number of the longest words, so a topic like "Kane to Bayern"
// resolves both the whole phrase and the individual names. The projection
// is deterministic and bounded; it runs exact folded matches only (no fuzzy
// or LLM matching), so it can never pull in the wrong entity.
func projectMentions(topic string) []entitymemory.Mention {
	mentions := []entitymemory.Mention{{Text: strings.TrimSpace(topic)}}
	fields := strings.Fields(topic)
	// Longest-first, capped: longer words are the more specific candidates.
	sort.SliceStable(fields, func(i, j int) bool { return len(fields[i]) > len(fields[j]) })
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(topic)): true}
	for _, f := range fields {
		if len(f) < 3 {
			continue
		}
		key := strings.ToLower(f)
		if seen[key] {
			continue
		}
		seen[key] = true
		mentions = append(mentions, entitymemory.Mention{Text: f})
		if len(mentions) >= 8 {
			break
		}
	}
	return mentions
}

func (s *storeMemoryService) PlannerHints(ctx context.Context, topic string) (string, []string, error) {
	mentions := projectMentions(topic)
	_, ids := s.store.ResolveMentions(mentions)
	if len(ids) == 0 {
		return "", nil, nil
	}
	return s.store.RenderPlannerHints(mentions, s.now, s.maxEntities), ids, nil
}

func (s *storeMemoryService) ResearcherHints(ctx context.Context, topic string) (string, []string, error) {
	mentions := projectMentions(topic)
	_, ids := s.store.ResolveMentions(mentions)
	if len(ids) == 0 {
		return "", nil, nil
	}
	return s.store.RenderResearcherHints(mentions, s.now, s.maxEntities), ids, nil
}

// Entities returns the full records for the given IDs (for the no-bypass
// check). Unknown IDs are skipped; the result is in input order.
func (s *storeMemoryService) Entities(ctx context.Context, ids []string) ([]entitymemory.Entity, error) {
	out := make([]entitymemory.Entity, 0, len(ids))
	for _, id := range ids {
		e, err := s.store.Get(id)
		if err != nil {
			continue // unknown ID: the check skips it rather than failing
		}
		out = append(out, *e)
	}
	return out, nil
}

// Close releases the underlying SQLite handle. It is idempotent (the backing
// store's Close is) and safe to call after load failures.
func (s *storeMemoryService) Close() error {
	return s.store.Store().Close()
}
