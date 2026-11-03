package memorystore

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// sanitizeAll is the test-only sentinel sanitizer: it performs no detection
// and never mutates records.
var sanitizeAll = Sanitizer(productionSanitizerDisabled{})

type productionSanitizerDisabled struct{}

func (productionSanitizerDisabled) Check(*memory.MemoryRecord) error { return nil }

// newTestStore returns a Store for the implementation under test, with
// detection disabled so tests can exercise secret-bearing content
// deterministically.
func newTestStore(t *testing.T, impl string, opts ...StoreOption) Store {
	t.Helper()
	switch impl {
	case "fake":
		return NewFakeStore().WithSanitizer(sanitizeAll)
	case "sqlite":
		opts = append(opts, WithSanitizer(sanitizeAll))
		s, _ := openTempStore(t, opts...)
		t.Cleanup(func() { _ = s.Close() })
		return s
	default:
		t.Fatalf("unknown implementation %q", impl)
	}
	return nil
}

// allImpls lists the implementations every Store test runs against.
var allImpls = []string{"fake", "sqlite"}

func testCRUDAllKinds(t *testing.T) {
	t.Helper()
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) {
			store := newTestStore(t, impl)
			ctx := context.Background()

			// Get on an empty store returns (nil, nil).
			rec, err := store.Get(ctx, "mem-does-not-exist")
			if err != nil {
				t.Fatalf("Get missing: %v", err)
			}
			if rec != nil {
				t.Fatalf("Get missing = %v, want nil", rec)
			}

			// Create each kind, then Get it back.
			for _, kind := range allKinds {
				r := buildRecord(t, kind, "")
				if err := store.Create(ctx, r); err != nil {
					t.Fatalf("Create %s: %v", kind, err)
				}
				got, err := store.Get(ctx, r.StableID)
				if err != nil {
					t.Fatalf("Get %s: %v", kind, err)
				}
				if got == nil {
					t.Fatalf("Get %s = nil after Create", kind)
				}
				if got.Kind != r.Kind || got.Scope != r.Scope {
					t.Errorf("round trip mismatch: got kind=%s scope=%s", got.Kind, got.Scope)
				}
			}

			// Duplicate Create fails with AlreadyExists.
			dup := buildRecord(t, memory.KindFactual, "")
			_ = store.Create(ctx, dup)
			if err := store.Create(ctx, dup); !IsAlreadyExists(err) {
				t.Errorf("duplicate Create: err = %v, want AlreadyExists", err)
			}

			// Write is idempotent for the same record.
			for i := 0; i < 3; i++ {
				if err := store.Write(ctx, dup); err != nil {
					t.Fatalf("Write %d: %v", i, err)
				}
			}
			n, err := store.Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if n != len(allKinds) {
				t.Errorf("Count after repeated Write = %d, want %d", n, len(allKinds))
			}

			// Delete
			if err := store.Delete(ctx, dup.StableID); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if err := store.Delete(ctx, dup.StableID); !IsNotFound(err) {
				t.Errorf("second Delete: err = %v, want NotFound", err)
			}
			n, _ = store.Count(ctx)
			if n != len(allKinds)-1 {
				t.Errorf("Count after Delete = %d, want %d", n, len(allKinds)-1)
			}
		})
	}
}

func TestCRUD(t *testing.T) { testCRUDAllKinds(t) }

// testQueryFilters runs the filtered-query matrix against an implementation.
func testQueryFilters(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()

	// Seed: two facts about the same person, one entity, one feedback,
	// one recipe execution, one suggestion, one correction. Records with
	// identical content collide on the deterministic ID, so the second
	// claim is varied.
	fact1 := buildRecord(t, memory.KindFactual, "")
	fact2 := buildRecord(t, memory.KindFactual, "")
	if err := fact2.EncodeValue(memory.FactualValue{
		Statement: "A second claim about the same person.",
		Data:      map[string]string{"team": "Other Team"},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	fact1.Subject = memory.EntityRef{Kind: "person", Name: "Jordan Rivers"}
	fact1.Field = "current_team"
	fact2.Subject = memory.EntityRef{Kind: "person", Name: "Jordan Rivers"}
	fact2.Field = "injury_status"
	// Re-derive: subject/field are part of the content digest, so the
	// modified records need their final (distinct) stable IDs.
	fact1.StableID = DeriveStableID(fact1)
	fact2.StableID = DeriveStableID(fact2)

	entity := buildRecord(t, memory.KindEntity, "")
	feedback := buildRecord(t, memory.KindEditorialFeedback, "")
	recipe := buildRecord(t, memory.KindRecipeExecution, "")
	suggestion := buildRecord(t, memory.KindSuggestion, "")
	correction := buildRecord(t, memory.KindCorrection, "")

	for _, r := range []*memory.MemoryRecord{fact1, fact2, entity, feedback, recipe, suggestion, correction} {
		if err := store.Write(ctx, r); err != nil {
			t.Fatalf("seed %s: %v", r.Kind, err)
		}
	}

	byFilter := func(q Query) []memory.MemoryStableID {
		recs, err := store.Query(ctx, q)
		if err != nil {
			t.Fatalf("Query %v: %v", q, err)
		}
		out := make([]memory.MemoryStableID, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.StableID)
		}
		return out
	}

	t.Run("byKind", func(t *testing.T) {
		got := byFilter(Query{Kind: memory.KindFactual, Status: StatusActive})
		if len(got) != 2 {
			t.Fatalf("kind=factual returned %d records, want 2", len(got))
		}
	})

	t.Run("bySubjectName", func(t *testing.T) {
		got := byFilter(Query{SubjectName: "Jordan Rivers", Status: StatusActive})
		if len(got) != 2 {
			t.Fatalf("subject name returned %d records, want 2", len(got))
		}
	})

	t.Run("bySubjectKind", func(t *testing.T) {
		got := byFilter(Query{SubjectKind: "person", Status: StatusActive})
		if len(got) != 2 {
			t.Fatalf("subject kind returned %d records, want 2", len(got))
		}
	})

	t.Run("byField", func(t *testing.T) {
		got := byFilter(Query{SubjectName: "Jordan Rivers", Field: "injury_status", Status: StatusActive})
		if len(got) != 1 || got[0] != fact2.StableID {
			t.Fatalf("field filter returned %v, want [%s]", got, fact2.StableID)
		}
	})

	t.Run("byScope", func(t *testing.T) {
		got := byFilter(Query{Scope: memory.ScopeProfile, Status: StatusActive})
		if len(got) != 1 || got[0] != feedback.StableID {
			t.Fatalf("scope filter returned %v, want [%s]", got, feedback.StableID)
		}
	})

	t.Run("combined", func(t *testing.T) {
		got := byFilter(Query{Kind: memory.KindFactual, SubjectName: "Jordan Rivers", Field: "current_team", Status: StatusActive})
		if len(got) != 1 || got[0] != fact1.StableID {
			t.Fatalf("combined filter returned %v, want [%s]", got, fact1.StableID)
		}
	})

	t.Run("limit", func(t *testing.T) {
		got := byFilter(Query{Status: StatusActive, Limit: 3})
		if len(got) != 3 {
			t.Fatalf("limit=3 returned %d records, want 3", len(got))
		}
	})

	t.Run("orderedByStableID", func(t *testing.T) {
		got := byFilter(Query{Status: StatusActive})
		sorted := make([]memory.MemoryStableID, len(got))
		copy(sorted, got)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		if !equalStrings(got, sorted) {
			t.Errorf("results not ordered by stable ID: %v vs %v", got, sorted)
		}
	})
}

func equalStrings(a, b []memory.MemoryStableID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueryFilters(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testQueryFilters(t, impl) })
	}
}

// testStaleQueries covers stale and expired memory queries.
func testStaleQueries(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()
	now := time.Now().UTC()

	// A recently-verified record: not stale. The freshness stamp is fixed
	// at build time (testNow), so the window is computed relative to
	// "now": a short window keeps this record fresh.
	fresh := buildRecord(t, memory.KindFactual, "")
	fresh.Freshness = memory.Freshness{
		LastVerifiedAt: fresh.Freshness.LastVerifiedAt,
		StaleAfter:     now.Sub(fresh.Freshness.LastVerifiedAt) + 2*time.Hour,
	}

	// A record past its window: stale. The freshness stamp must not be
	// after updated_at (record invariant): encode the record with a
	// build-time verification stamp first, then narrow the window to 1h.
	stale := buildRecord(t, memory.KindFactual, "")
	if err := stale.EncodeValue(memory.FactualValue{
		Statement: "An old claim about the person.",
		Data:      map[string]string{"team": "Old Team"},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	stale.Field = "old_fact"
	stale.Freshness = memory.Freshness{
		LastVerifiedAt: stale.Freshness.LastVerifiedAt,
		StaleAfter:     1 * time.Hour,
	}
	if err := stale.Validate(); err != nil {
		t.Fatalf("stale record invalid: %v", err)
	}
	stale.StableID = DeriveStableID(stale)

	// A long-unverified record: stale (window shorter than verification age).
	// The freshness stamp must not be after updated_at (record invariant),
	// so the stale record is encoded with a build-time verification stamp.
	never := buildRecord(t, memory.KindFactual, "")
	if err := never.EncodeValue(memory.FactualValue{
		Statement: "Long-unverified claim.",
	}, testNow); err != nil {
		t.Fatal(err)
	}
	never.Field = "unverified"
	never.Freshness = memory.Freshness{
		LastVerifiedAt: never.Freshness.LastVerifiedAt,
		StaleAfter:     1 * time.Hour,
	}
	if err := never.Validate(); err != nil {
		t.Fatalf("never record invalid: %v", err)
	}
	never.StableID = DeriveStableID(never)

	// An expired record: active lifecycle but past its absolute expiry.
	// expires_at must not be before created_at (record invariant): the
	// record was created a week ago and expires an hour ago.
	expired := buildRecord(t, memory.KindFactual, "")
	expired.CreatedAt = now.Add(-7 * 24 * time.Hour)
	expired.UpdatedAt = now.Add(-24 * time.Hour)
	expired.Field = "expired_fact"
	expired.Expiration.ExpiresAt = now.Add(-time.Hour)
	if err := expired.Validate(); err != nil {
		t.Fatalf("expired record invalid: %v", err)
	}
	expired.StableID = DeriveStableID(expired)

	for _, r := range []*memory.MemoryRecord{fresh, stale, never, expired} {
		if err := store.Write(ctx, r); err != nil {
			t.Fatalf("seed %s: %v", r.StableID, err)
		}
	}

	t.Run("staleOnly", func(t *testing.T) {
		got, err := store.Query(ctx, Query{StaleAt: now, Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		ids := map[memory.MemoryStableID]bool{}
		for _, r := range got {
			ids[r.StableID] = true
		}
		if ids[fresh.StableID] {
			t.Error("fresh record returned by stale query")
		}
		if !ids[stale.StableID] || !ids[never.StableID] {
			t.Errorf("stale records missing: stale=%v never=%v", ids[stale.StableID], ids[never.StableID])
		}
	})

	t.Run("expiredExcludedFromActive", func(t *testing.T) {
		got, err := store.Query(ctx, Query{Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			if r.StableID == expired.StableID {
				t.Error("expired record returned by active query")
			}
		}
	})

	t.Run("expiredVisibleInAny", func(t *testing.T) {
		got, err := store.Query(ctx, Query{Status: StatusAny})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range got {
			if r.StableID == expired.StableID {
				found = true
			}
		}
		if !found {
			t.Error("expired record missing from status=any query")
		}
	})
}

func TestStaleQueries(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testStaleQueries(t, impl) })
	}
}

// testLifecycle covers supersession and invalidation semantics.
func testLifecycle(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()

	old := buildRecord(t, memory.KindFactual, "")
	new := buildRecord(t, memory.KindFactual, "")
	new.StableID = DeriveStableID(new)
	_ = new.EncodeValue(memory.FactualValue{Statement: "The corrected claim."}, testNow)

	if err := store.Write(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, new); err != nil {
		t.Fatal(err)
	}

	t.Run("supersede", func(t *testing.T) {
		updated, err := store.Supersede(ctx, string(old.StableID), string(new.StableID), "re-verified with newer source")
		if err != nil {
			t.Fatalf("Supersede: %v", err)
		}
		if updated.Expiration.InvalidationStatus != memory.InvalidationSuperseded {
			t.Errorf("status = %s, want superseded", updated.Expiration.InvalidationStatus)
		}
		if updated.Expiration.SupersededBy != string(new.StableID) {
			t.Errorf("superseded_by = %s, want %s", updated.Expiration.SupersededBy, new.StableID)
		}
		if updated.IsUsable(time.Now().UTC()) {
			t.Error("superseded record must not be usable")
		}

		// Persisted and re-loaded with the transition.
		got, err := store.Get(ctx, old.StableID)
		if err != nil || got == nil {
			t.Fatalf("Get after supersede: %v %v", got, err)
		}
		if got.Expiration.InvalidationStatus != memory.InvalidationSuperseded {
			t.Errorf("persisted status = %s, want superseded", got.Expiration.InvalidationStatus)
		}
	})

	t.Run("supersedeNonActive", func(t *testing.T) {
		// The old record is now superseded; superseding it again must fail.
		_, err := store.Supersede(ctx, string(old.StableID), string(new.StableID), "again")
		if !IsSupersedeError(err) {
			t.Errorf("second Supersede: err = %v, want SupersedeError", err)
		}
	})

	t.Run("supersedeEmptyNewID", func(t *testing.T) {
		other := buildRecord(t, memory.KindFactual, "")
		_ = store.Write(ctx, other)
		_, err := store.Supersede(ctx, string(other.StableID), "", "x")
		if !IsSupersedeError(err) {
			t.Errorf("Supersede with empty new ID: err = %v, want SupersedeError", err)
		}
	})

	t.Run("supersedeMissing", func(t *testing.T) {
		_, err := store.Supersede(ctx, "mem-ghost", "mem-x", "x")
		if !IsNotFound(err) {
			t.Errorf("Supersede missing record: err = %v, want NotFound", err)
		}
	})

	t.Run("invalidate", func(t *testing.T) {
		victim := buildRecord(t, memory.KindFactual, "")
		if err := store.Write(ctx, victim); err != nil {
			t.Fatal(err)
		}
		updated, err := store.Invalidate(ctx, victim.StableID, "found to be wrong")
		if err != nil {
			t.Fatalf("Invalidate: %v", err)
		}
		if updated.Expiration.InvalidationStatus != memory.InvalidationInvalidated {
			t.Errorf("status = %s, want invalidated", updated.Expiration.InvalidationStatus)
		}
		if updated.IsUsable(time.Now().UTC()) {
			t.Error("invalidated record must not be usable")
		}

		got, _ := store.Get(ctx, victim.StableID)
		if got.Expiration.InvalidationReason != "found to be wrong" {
			t.Errorf("persisted reason = %q", got.Expiration.InvalidationReason)
		}
	})

	t.Run("invalidateNonActive", func(t *testing.T) {
		_, err := store.Invalidate(ctx, old.StableID, "again")
		if !IsInvalidateError(err) {
			t.Errorf("Invalidate non-active: err = %v, want InvalidateError", err)
		}
	})

	t.Run("invalidateMissing", func(t *testing.T) {
		_, err := store.Invalidate(ctx, "mem-ghost", "x")
		if !IsNotFound(err) {
			t.Errorf("Invalidate missing: err = %v, want NotFound", err)
		}
	})

	t.Run("lifecycleExcludedFromActiveQueries", func(t *testing.T) {
		got, err := store.Query(ctx, Query{Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		ids := map[memory.MemoryStableID]bool{}
		for _, r := range got {
			ids[r.StableID] = true
		}
		if ids[old.StableID] {
			t.Error("superseded record visible in active query")
		}
		anyGot, err := store.Query(ctx, Query{Status: StatusAny})
		if err != nil {
			t.Fatal(err)
		}
		anyIDs := map[memory.MemoryStableID]bool{}
		for _, r := range anyGot {
			anyIDs[r.StableID] = true
		}
		if !anyIDs[old.StableID] {
			t.Error("superseded record missing from status=any query")
		}
	})
}

func TestLifecycle(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testLifecycle(t, impl) })
	}
}

// testWriteGates covers the pre-write invariants: invalid records and
// secret-laden records are refused.
func testWriteGates(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()

	t.Run("refusesNilRecord", func(t *testing.T) {
		if err := store.Write(ctx, nil); !IsInvalidRecord(err) {
			t.Errorf("Write(nil): err = %v, want InvalidRecord", err)
		}
	})

	t.Run("refusesInvalidRecord", func(t *testing.T) {
		rec := buildRecord(t, memory.KindFactual, "")
		rec.SchemaVersion = "garbage"
		if err := store.Write(ctx, rec); !IsInvalidRecord(err) {
			t.Errorf("Write(invalid): err = %v, want InvalidRecord", err)
		}
	})

	t.Run("refusesForeignSchemaMajor", func(t *testing.T) {
		rec := buildRecord(t, memory.KindFactual, "")
		rec.SchemaVersion = "9.0.0"
		if err := store.Write(ctx, rec); !IsInvalidRecord(err) {
			t.Errorf("Write(foreign major): err = %v, want InvalidRecord", err)
		}
	})

	t.Run("refusesUnprefixedID", func(t *testing.T) {
		rec := buildRecord(t, memory.KindFactual, "")
		rec.StableID = "no-prefix-id"
		if err := store.Write(ctx, rec); !IsInvalidRecord(err) {
			t.Errorf("Write(bad prefix): err = %v, want InvalidRecord", err)
		}
	})
}

func TestWriteGates(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testWriteGates(t, impl) })
	}
}

// testRoundTripByKind verifies that every kind survives a write/get cycle
// with its payload intact.
func testRoundTripByKind(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()

	for _, kind := range allKinds {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			rec := buildRecord(t, kind, "")
			if err := store.Write(ctx, rec); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := store.Get(ctx, rec.StableID)
			if err != nil || got == nil {
				t.Fatalf("Get: %v %v", got, err)
			}
			if got.Kind != kind {
				t.Fatalf("kind = %s, want %s", got.Kind, kind)
			}
			if string(got.Value) != string(rec.Value) {
				t.Errorf("value payload differs after round trip:\n got %s\nwant %s", got.Value, rec.Value)
			}
			// The payload must still decode into the kind's type.
			if _, err := got.DecodeValue(); err != nil {
				t.Errorf("decoded value fails: %v", err)
			}
		})
	}
}

func TestRoundTripByKind(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testRoundTripByKind(t, impl) })
	}
}

// testValueFidelity checks the structured payload details (subjects,
// provenance, freshness, expiration) survive a round trip.
func testValueFidelity(t *testing.T, impl string) {
	t.Helper()
	store := newTestStore(t, impl)
	ctx := context.Background()

	rec := buildRecord(t, memory.KindFactual, "")
	rec.Subject = memory.EntityRef{
		Kind: "person", Name: "Jordan Rivers", CanonicalID: "person-1",
		Aliases: []string{"JR", "Jordan R."},
	}
	rec.Freshness = memory.Freshness{LastVerifiedAt: testNow, StaleAfter: 72 * time.Hour}
	rec.Expiration.ExpiresAt = testNow.Add(30 * 24 * time.Hour)
	rec.Provenance = memory.Provenance{
		Sources: []memory.MemorySource{
			{SourceType: "web", SourceID: "https://example.com/rivers", Title: "Rivers profile", RetrievedAt: testNow, Note: "roster section"},
			{SourceType: "rss", SourceID: "feed://sharks", Title: "Sharks feed", RetrievedAt: testNow},
		},
		DerivedFrom:   []string{"mem-source-1"},
		VerifiedBy:    "verifier-run-9",
		UserCorrected: true,
		CorrectedBy:   "editor",
		CorrectedAt:   testNow.Add(time.Hour),
	}
	rec.UpdatedAt = testNow.Add(time.Hour)
	if err := store.Write(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, rec.StableID)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Subject.Kind != "person" || got.Subject.Name != "Jordan Rivers" || got.Subject.CanonicalID != "person-1" {
		t.Errorf("subject not preserved: %+v", got.Subject)
	}
	if len(got.Subject.Aliases) != 2 || got.Subject.Aliases[0] != "JR" {
		t.Errorf("aliases not preserved: %v", got.Subject.Aliases)
	}
	if !got.Freshness.LastVerifiedAt.Equal(testNow) {
		t.Errorf("last_verified_at = %v, want %v", got.Freshness.LastVerifiedAt, testNow)
	}
	if got.Freshness.StaleAfter != 72*time.Hour {
		t.Errorf("stale_after = %v, want 72h", got.Freshness.StaleAfter)
	}
	if len(got.Provenance.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(got.Provenance.Sources))
	}
	if got.Provenance.Sources[0].Title != "Rivers profile" || got.Provenance.Sources[1].SourceType != "rss" {
		t.Errorf("sources not preserved: %+v", got.Provenance.Sources)
	}
	if !got.Provenance.UserCorrected || got.Provenance.CorrectedBy != "editor" {
		t.Errorf("correction provenance not preserved: %+v", got.Provenance)
	}
	if !got.Expiration.ExpiresAt.Equal(testNow.Add(30 * 24 * time.Hour)) {
		t.Errorf("expires_at = %v", got.Expiration.ExpiresAt)
	}
}

func TestValueFidelity(t *testing.T) {
	for _, impl := range allImpls {
		t.Run(impl, func(t *testing.T) { testValueFidelity(t, impl) })
	}
}

var _ = errors.New
