package entitymemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

// Store is a provenance-aware, reusable entity memory.
//
// It persists entities as v0.7 memory records (KindEntity, ScopeEntity)
// through a memorystore.Store and keeps an in-memory index of entity IDs,
// canonical names, and aliases so that resolution is deterministic and does
// not scan the whole store per mention.
//
// Persistence is best-effort and degrades safely: if the backing store
// refuses a write (sanitization gate, storage failure), the in-memory state
// is still updated and the error is reported to the caller. On every open,
// the store loads its prior records from the backing store, so memory that
// survived the previous run is reused by the next one.
//
// A Store is safe for concurrent use: all methods take a lock.
type Store struct {
	mu       sync.RWMutex
	nowAt    time.Time // reference time for staleness decisions (see WithNow)
	store    memorystore.Store
	entities map[string]*Entity  // by canonical entity ID
	byName   map[string][]string // folded canonical name / alias -> entity IDs
	loadErr  error               // set if the open-time load failed (partial or empty state)

	// policy is the freshness policy for attribute-level freshness
	// judgments (FreshnessPolicy returns the backing store's policy when
	// this is unset).
	policy memory.FreshnessPolicy

	// policyKnown is true once SetFreshnessPolicy has been called.
	policyKnown bool
}

// NewStore opens the entity memory on top of the given v0.7 memory store.
//
// The backing store is typically a memorystore.SQLiteStore (durable,
// reusable across runs) or a memorystore.FakeStore (tests). NewStore loads
// every entity record it finds; records that fail to decode are skipped and
// reported through LoadError (the store still opens with the rest).
func NewStore(store memorystore.Store) *Store {
	s := &Store{
		store:    store,
		entities: make(map[string]*Entity),
		byName:   make(map[string][]string),
	}
	s.load(context.Background())
	return s
}

// Store returns the backing v0.7 memory store.
func (s *Store) Store() memorystore.Store {
	return s.store
}

// WithNow sets the reference time used for staleness decisions (e.g. in
// Resolve and the stale-aware tie-break) and returns the store, so callers
// can chain: NewStore(...).WithNow(runTime).
//
// A zero time disables staleness judgments: matches are never reported
// stale. All other behavior is unaffected.
func (s *Store) WithNow(t time.Time) *Store {
	s.mu.Lock()
	s.nowAt = memory.TimeToUTC(t)
	s.mu.Unlock()
	return s
}

// SetFreshnessPolicy overrides the freshness policy used for attribute-level
// freshness judgments (FreshnessStatus, StaleAttributes).
//
// The policy is generic (volatility bands + durations; see
// memory.FreshnessPolicy). When unset, the store defers to the backing
// memory store's policy (FreshnessPolicy) and falls back to
// memory.DefaultFreshnessPolicy, so out of the box an attribute's
// volatility band drives its freshness window.
func (s *Store) SetFreshnessPolicy(p memory.FreshnessPolicy) *Store {
	s.mu.Lock()
	s.policy = p
	s.policyKnown = true
	s.mu.Unlock()
	return s
}

// FreshnessPolicy returns the freshness policy in effect for attribute
// freshness judgments: the store's own override when one has been set, else
// the backing store's policy, else the default policy.
func (s *Store) FreshnessPolicy() memory.FreshnessPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policyLocked()
}

// now returns the reference time for staleness decisions (zero disables
// them). Caller must hold s.mu (read or write).
func (s *Store) now() time.Time {
	return s.nowAt
}

// policyLocked returns the freshness policy in effect (see
// FreshnessPolicy). Caller must hold s.mu (read or write).
func (s *Store) policyLocked() memory.FreshnessPolicy {
	if s.policyKnown {
		return s.policy
	}
	if p, ok := s.store.(interface{ FreshnessPolicy() memory.FreshnessPolicy }); ok {
		return p.FreshnessPolicy()
	}
	return memory.DefaultFreshnessPolicy
}

// LoadError returns the error from the open-time load, if any (e.g. a
// backing-store failure that left the in-memory state empty or partial).
func (s *Store) LoadError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadErr
}

// load refreshes the in-memory state from the backing store.
func (s *Store) load(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entities = make(map[string]*Entity)
	s.byName = make(map[string][]string)
	s.loadErr = nil

	q := memorystore.Query{
		Kind:   recordKind,
		Status: memorystore.StatusAny, // include superseded/invalidated: the loader filters by entity-level state
	}
	recs, err := s.store.Query(ctx, q)
	if err != nil {
		s.loadErr = fmt.Errorf("entitymemory: load from backing store: %w", err)
		return
	}
	for _, rec := range recs {
		id, ok := entityIDFromRecord(rec)
		if !ok {
			continue // not an entity record written by this package
		}
		e, err := entityFromRecord(rec)
		if err != nil {
			s.loadErr = errors.Join(s.loadErr, fmt.Errorf("entitymemory: decode %s: %w", id, err))
			continue
		}
		e.memoryID = rec.StableID
		s.entities[id] = e
		s.indexLocked(e)
	}
}

// entityIDFromRecord extracts the entity ID from a record written by this
// package (record.Field = "entity:<id>").
func entityIDFromRecord(rec *memory.MemoryRecord) (string, bool) {
	id, ok := strings.CutPrefix(rec.Field, recordPrefix)
	return id, ok && id != ""
}

// ErrDuplicateName is returned when creating or aliasing an entity whose
// canonical name or alias is already used by a *different* entity. The
// ticket's safety rule: same names that may refer to different things are
// neither merged nor stolen — the collision is surfaced and the caller must
// disambiguate explicitly.
var ErrDuplicateName = errors.New("name is already used by another entity")

// ErrEntityExists is returned by Create when the entity ID is already known.
var ErrEntityExists = errors.New("entity already exists")

// ErrEntityNotFound is returned by lookup and update operations on unknown
// entity IDs.
var ErrEntityNotFound = errors.New("entity not found")

// ErrAttributeNotFound is returned when an attribute key is requested that
// does not exist on the entity.
var ErrAttributeNotFound = errors.New("attribute not found")

// ErrConflict describes a field-level conflict: new data disagrees with
// existing (unexpired) memory for the same attribute. The existing value is
// reported so the caller can decide; nothing is overwritten.
type ErrConflict struct {
	EntityID    string
	Key         string
	Existing    string
	ExistingAt  time.Time
	New         string
	NewVerified time.Time
	Reason      string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("conflict on entity %s attribute %q: existing %q (verified %s) vs new %q (verified %s): %s",
		e.EntityID, e.Key, e.Existing, formatWhen(e.ExistingAt), e.New, formatWhen(e.NewVerified), e.Reason)
}

// IsConflict checks whether err is an ErrConflict (including wrapped).
func IsConflict(err error) bool {
	var c *ErrConflict
	return errors.As(err, &c)
}

// ErrSecretDetected re-exposes the memorystore secret-gate base error so
// callers can classify refusals without importing memorystore directly.
var ErrSecretDetected = memorystore.ErrSecretDetected

// IsSecretDetected reports whether err is a secret-gate refusal.
func IsSecretDetected(err error) bool {
	return memorystore.IsSecretDetected(err)
}

// Create adds a new entity to the memory.
//
// Guarantees:
//   - the entity's invariants (Validate) hold; CreatedAt and UpdatedAt are
//     auto-stamped from now when left zero. LastVerifiedAt (if set) must
//     not be after now, which the stamping enforces through Validate.
//   - its ID is not already known (ErrEntityExists);
//   - none of its canonical name or aliases is already used by a *different*
//     entity (ErrDuplicateName — duplicate names are refused, never
//     silently merged);
//   - the record passes the backing store's sanitization gate (the error is
//     returned; IsSecretDetected classifies gate refusals). On a write
//     failure the in-memory entity is still registered.
func (s *Store) Create(ctx context.Context, now time.Time, e *Entity) error {
	now = memory.TimeToUTC(now)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entities[e.ID]; ok {
		return fmt.Errorf("%w: %s", ErrEntityExists, e.ID)
	}
	for _, err := range s.nameConflictsLocked(e) {
		if err != nil {
			return err
		}
	}
	if err := s.persistLocked(ctx, now, e); err != nil {
		return err
	}
	return nil
}

// createAllowingNameDups is Create minus the cross-entity name-uniqueness
// check. It exists for two purposes: loading/merging external or legacy
// entity data that may carry duplicate names (the ambiguity machinery is
// what keeps those safe at resolution time), and test fixtures that
// exercise same-name ambiguity.
//
// All other Create guarantees apply (invariants, ID uniqueness,
// sanitization gate).
func (s *Store) createAllowingNameDups(ctx context.Context, now time.Time, e *Entity) error {
	now = memory.TimeToUTC(now)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entities[e.ID]; ok {
		return fmt.Errorf("%w: %s", ErrEntityExists, e.ID)
	}
	return s.persistLocked(ctx, now, e)
}

// persistLocked stamps timestamps, writes the record to the backing store,
// and refreshes the in-memory index. Caller must hold s.mu.
//
// The record's stable memory ID is assigned once (on first persist) and
// reused for every later write of the same entity, so the backing store
// updates the same record in place instead of accumulating history rows.
//
// On a backing-store failure the entity is still indexed in-memory (with
// the reported error) so a transient storage problem does not silently lose
// the memory for the rest of the run; the error surfaces to the operator.
func (s *Store) persistLocked(ctx context.Context, now time.Time, e *Entity) error {
	now = memory.TimeToUTC(now)
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	} else {
		e.UpdatedAt = memory.TimeToUTC(e.UpdatedAt)
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = e.UpdatedAt
	}
	if existing, ok := s.entities[e.ID]; ok {
		if string(e.memoryID) == "" {
			e.memoryID = existing.memoryID
		}
	}
	rec := e.record(e.UpdatedAt)
	if string(e.memoryID) == "" {
		e.memoryID = memorystore.DeriveStableID(rec)
	}
	rec.StableID = e.memoryID
	if err := s.store.Write(ctx, rec); err != nil {
		// Degrade: keep the in-memory copy, report the failure.
		s.entities[e.ID] = e
		s.indexLocked(e)
		return fmt.Errorf("entitymemory: persist %s: %w", e.ID, err)
	}
	s.indexLocked(e)
	return nil
}

// nameConflictsLocked returns one error per name of e that collides with a
// different entity. Caller must hold s.mu.
func (s *Store) nameConflictsLocked(e *Entity) []error {
	var errs []error
	checked := make(map[string]bool)
	check := func(name string) {
		f := foldName(name)
		if f == "" || checked[f] {
			return
		}
		checked[f] = true
		for _, id := range s.byName[f] {
			if id != e.ID {
				errs = append(errs, fmt.Errorf("%w: %q is used by both %s and %s", ErrDuplicateName, name, id, e.ID))
			}
		}
	}
	check(e.CanonicalName)
	for _, a := range e.Aliases {
		check(a)
	}
	return errs
}

// indexLocked inserts or refreshes e in the in-memory indexes. Caller must
// hold s.mu.
func (s *Store) indexLocked(e *Entity) {
	s.unindexLocked(e.ID)
	s.entities[e.ID] = e
	add := func(name, id string) {
		f := foldName(name)
		if f == "" {
			return
		}
		list := s.byName[f]
		for _, existing := range list {
			if existing == id {
				return
			}
		}
		s.byName[f] = append(list, id)
	}
	add(e.CanonicalName, e.ID)
	for _, a := range e.Aliases {
		add(a, e.ID)
	}
}

// unindexLocked removes all index entries pointing at the given ID. Caller
// must hold s.mu.
func (s *Store) unindexLocked(id string) {
	for f, list := range s.byName {
		kept := list[:0]
		for _, x := range list {
			if x != id {
				kept = append(kept, x)
			}
		}
		if len(kept) == 0 {
			delete(s.byName, f)
		} else {
			s.byName[f] = kept
		}
	}
}

// Get returns a copy of the entity with the given canonical ID, or
// ErrEntityNotFound.
func (s *Store) Get(id string) (*Entity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	return e.clone(), nil
}

// Has reports whether the entity ID is known.
func (s *Store) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.entities[id]
	return ok
}

// IDCount returns the number of known entities.
func (s *Store) IDCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entities)
}

// All returns copies of every known entity, ordered by canonical entity ID
// (deterministic).
func (s *Store) All() []*Entity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Entity, 0, len(s.entities))
	for _, id := range sortedKeysString(s.entities) {
		out = append(out, s.entities[id].clone())
	}
	return out
}
