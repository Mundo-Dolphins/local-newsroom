package memorystore

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// FakeStore is an in-memory Store implementation for tests and for
// consumers that do not need durable storage.
//
// It mirrors the SQLiteStore semantics: idempotent Write, strict Create,
// deterministic order in Query, atomic lifecycle transitions, and a
// sanitization gate.
type FakeStore struct {
	mu        sync.RWMutex
	records   map[memory.MemoryStableID]*memory.MemoryRecord
	sanitizer Sanitizer
	schemaVer int
}

// NewFakeStore returns an empty FakeStore with the default (pattern-only)
// sanitizer.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		records:   make(map[memory.MemoryStableID]*memory.MemoryRecord),
		sanitizer: NewProductionSanitizer(),
		schemaVer: currentSchemaVersion,
	}
}

// WithSanitizer returns a copy of the store configured with a custom
// sanitizer (for tests that need to control detection).
func (s *FakeStore) WithSanitizer(sz Sanitizer) *FakeStore {
	clone := &FakeStore{
		records:   make(map[memory.MemoryStableID]*memory.MemoryRecord),
		sanitizer: sz,
		schemaVer: s.schemaVer,
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, rec := range s.records {
		clone.records[id] = cloneRecord(rec)
	}
	return clone
}

// Ensure FakeStore implements Store
var _ Store = (*FakeStore)(nil)

// Write implements Store (upsert).
func (s *FakeStore) Write(_ context.Context, rec *memory.MemoryRecord) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	if s.sanitizer != nil {
		if err := s.sanitizer.Check(rec); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rec.StableID
	if existing, ok := s.records[key]; ok {
		existing.CreatedAt = rec.CreatedAt
	}
	s.records[key] = cloneRecord(rec)
	return nil
}

// Create implements Store (strict insert).
func (s *FakeStore) Create(ctx context.Context, rec *memory.MemoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[rec.StableID]; ok {
		return AlreadyExistsError{ID: rec.StableID}
	}
	return s.writeLocked(ctx, rec)
}

// writeLocked performs the gate + insert while the caller holds the lock.
func (s *FakeStore) writeLocked(_ context.Context, rec *memory.MemoryRecord) error {
	if err := validateRecord(rec); err != nil {
		return err
	}
	if s.sanitizer != nil {
		if err := s.sanitizer.Check(rec); err != nil {
			return err
		}
	}
	s.records[rec.StableID] = cloneRecord(rec)
	return nil
}

// Get implements Store. Returns (nil, nil) if absent.
func (s *FakeStore) Get(_ context.Context, id memory.MemoryStableID) (*memory.MemoryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	return cloneRecord(rec), nil
}

// Delete implements Store.
func (s *FakeStore) Delete(_ context.Context, id memory.MemoryStableID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return NotFoundError{ID: id}
	}
	delete(s.records, id)
	return nil
}

// Query implements Store.
func (s *FakeStore) Query(_ context.Context, q Query) ([]*memory.MemoryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*memory.MemoryRecord
	for _, rec := range s.records {
		if !q.matches(rec) {
			continue
		}
		out = append(out, cloneRecord(rec))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StableID < out[j].StableID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Supersede implements Store.
func (s *FakeStore) Supersede(_ context.Context, oldID, newID, reason string) (*memory.MemoryRecord, error) {
	if newID == "" {
		return nil, SupersedeError{ID: memory.MemoryStableID(oldID), Reason: "new ID is empty"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionLocked(memory.MemoryStableID(oldID), memory.InvalidationSuperseded, newID, reason, false)
}

// Invalidate implements Store.
func (s *FakeStore) Invalidate(_ context.Context, id memory.MemoryStableID, reason string) (*memory.MemoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transitionLocked(id, memory.InvalidationInvalidated, "", reason, true)
}

// transitionLocked applies a lifecycle transition; the caller must hold s.mu
// (write lock). isInvalidate selects the error type for non-active records.
func (s *FakeStore) transitionLocked(
	id memory.MemoryStableID,
	to memory.InvalidationStatus,
	target, reason string,
	isInvalidate bool,
) (*memory.MemoryRecord, error) {
	rec, ok := s.records[id]
	if !ok {
		return nil, NotFoundError{ID: id}
	}
	if rec.Expiration.InvalidationStatus != memory.InvalidationActive {
		why := "record is " + string(rec.Expiration.InvalidationStatus) + ", not active"
		if isInvalidate {
			return nil, InvalidateError{ID: id, Reason: why}
		}
		return nil, SupersedeError{ID: id, Reason: why}
	}
	rec.Expiration.InvalidationStatus = to
	rec.Expiration.SupersededBy = target
	rec.Expiration.InvalidationReason = reason
	rec.UpdatedAt = time.Now().UTC()
	return cloneRecord(rec), nil
}

// Count implements Store.
func (s *FakeStore) Count(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records), nil
}

// SchemaVersion implements Store.
func (s *FakeStore) SchemaVersion(_ context.Context) (int, error) {
	return s.schemaVer, nil
}

// Close implements Store (no-op for the fake).
func (s *FakeStore) Close() error { return nil }

// matches reports whether the record satisfies the query filter.
func (q Query) matches(rec *memory.MemoryRecord) bool {
	if q.Kind != "" && rec.Kind != q.Kind {
		return false
	}
	if q.Scope != "" && rec.Scope != q.Scope {
		return false
	}
	if q.SubjectKind != "" && rec.Subject.Kind != q.SubjectKind {
		return false
	}
	if q.SubjectName != "" && rec.Subject.Name != q.SubjectName {
		return false
	}
	if q.SubjectID != "" && rec.Subject.CanonicalID != q.SubjectID {
		return false
	}
	if q.Field != "" && rec.Field != q.Field {
		return false
	}
	switch q.Status {
	case StatusActive:
		if !rec.IsUsable(time.Now().UTC()) {
			return false
		}
	case StatusAny:
		// include all
	}
	if !q.StaleAt.IsZero() && !rec.Freshness.IsStale(q.StaleAt) {
		return false
	}
	return true
}

// cloneRecord deep-clones a record (the record is shared mutable state in
// the fake; the value payload is copied byte-wise).
func cloneRecord(rec *memory.MemoryRecord) *memory.MemoryRecord {
	if rec == nil {
		return nil
	}
	out := *rec
	out.Value = append([]byte(nil), rec.Value...)
	out.Subject.Aliases = append([]string(nil), rec.Subject.Aliases...)
	out.Provenance.Sources = append([]memory.MemorySource(nil), rec.Provenance.Sources...)
	out.Provenance.DerivedFrom = append([]string(nil), rec.Provenance.DerivedFrom...)
	return &out
}
