// Package memorystore defines the persistence contracts for the v0.7
// editorial-memory subsystem (issue #142).
//
// It is the storage half of the editorial memory story: a Store interface
// over the versioned records of internal/memory, plus a SQLite
// implementation (SQLiteStore) and an in-memory fake for tests
// (FakeStore). The implementation is intentionally separate from the
// archive/RAG store (internal/archive): the archive answers "what did source
// documents say?", while this store answers "what has the newsroom learned
// previously?". The two databases are independent; memory never reuses
// archive types and archive retrieval never returns memory as if it were a
// source document.
//
// Core principle (inherited from internal/memory): a MemoryRecord is
// contextual knowledge, not verified evidence. The persistence layer adds
// no behavior that would bypass the verification pipeline: retrieval here
// returns memory records, and any consumer that needs evidence must still
// run claims through the normal pipeline.
//
// Implementation notes (SQLiteStore):
//   - Versioned schema migrations (embedded SQL, applied in transactions)
//   - Atomic writes: every mutating operation runs in a transaction
//   - Safe concurrent local use: WAL mode, busy timeout, bounded connection
//     pool (mirrors internal/archive's SQLiteStore)
//   - Deterministic IDs: DeriveStableID is a pure function of the record's
//     content; the same record always produces the same stable ID
//   - Secret-safe: records pass a sanitization gate before persist
//     (fail closed: content that still contains a detected secret is
//     refused)
//   - Path containment: the database path is resolved and constrained to a
//     directory; the file and its sidecars get private permissions
package memorystore

import (
	"context"
	"errors"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// Store is the persistence interface for editorial memory.
//
// Implementations should:
//   - Be concurrency-safe for local multi-goroutine use
//   - Use deterministic IDs so re-deriving the same record content yields the
//     same stable ID
//   - Perform every mutation in a single transaction (atomic writes)
//   - Refuse to persist records that fail the sanitization gate
//
// Records that do not exist return (nil, nil)-style results through the
// NotFound error helpers, mirroring internal/archive.
type Store interface { // Write inserts or replaces a record identified by its StableID.
	//
	// Write is idempotent for byte-identical records: rewriting the same
	// record is a no-op apart from a refresh of UpdatedAt. Create is the
	// strict variant for first-time inserts.
	//
	// Write applies the sanitization gate: a record whose serialized form
	// still contains a detected secret is refused (SecretDetectedError).
	Write(ctx context.Context, rec *memory.MemoryRecord) error

	// Create inserts a record that must not already exist.
	//
	// Returns AlreadyExistsError if a record with the same StableID is
	// present. Sanitization applies as in Write.
	Create(ctx context.Context, rec *memory.MemoryRecord) error

	// Get retrieves a record by its stable ID.
	//
	// Returns (nil, nil) if the record does not exist.
	Get(ctx context.Context, id memory.MemoryStableID) (*memory.MemoryRecord, error)

	// Delete removes a record by its stable ID.
	//
	// Returns NotFoundError if the record does not exist. Deletion is
	// physical: memory.db is a local working set, not an audit archive;
	// supersession (below) is the auditable lifecycle operation.
	Delete(ctx context.Context, id memory.MemoryStableID) error

	// Query filtered retrieval. All filter fields are optional; the empty
	// filter returns every record. Records are returned in deterministic
	// order (by stable ID).
	Query(ctx context.Context, q Query) ([]*memory.MemoryRecord, error)

	// Supersede marks an old record as superseded by newID, atomically
	// updating the old record's expiration block. It returns the updated
	// old record.
	//
	// Returns NotFoundError if oldID does not exist or newID is empty, and
	// SupersedeError if the old record is not in the active state (a
	// superseded or invalidated record cannot be superseded again).
	Supersede(ctx context.Context, oldID, newID, reason string) (*memory.MemoryRecord, error)

	// Invalidate marks an active record as invalidated, with a
	// human-readable reason. It returns the updated record.
	//
	// Returns NotFoundError if the record does not exist and InvalidateError
	// if the record is not in the active state.
	Invalidate(ctx context.Context, id memory.MemoryStableID, reason string) (*memory.MemoryRecord, error)

	// Count returns the number of stored records.
	Count(ctx context.Context) (int, error)

	// FreshnessPolicy returns the freshness policy the store applies to
	// query-time freshness filters (Query.FreshStatus/FreshnessAt).
	// The returned policy is immutable and may be shared.
	FreshnessPolicy() memory.FreshnessPolicy

	// SetFreshnessPolicy replaces the policy used by freshness-filtered
	// queries. The policy is copied: later mutation of the argument does not
	// affect the store. A zero-value policy behaves like the default
	// (memory.DefaultFreshnessPolicy), since nil band maps fall back to the
	// defaults.
	SetFreshnessPolicy(p memory.FreshnessPolicy)

	// SchemaVersion returns the current schema version of the backing store.
	//
	// Migration bookkeeping is internal to the implementation; callers use
	// this to report the store's version (e.g. in a future
	// `newsroom memory doctor` command).
	SchemaVersion(ctx context.Context) (int, error)

	// Close releases the store's resources (database connection, ...).
	Close() error
}

// Query is the filter for filtered retrieval.
//
// Every field is optional; the empty filter matches all records. Fields
// combine with AND semantics:
//
//	Kind         exact match on record kind (factual, entity, ...)
//	Scope        exact match on record scope (run, source, ...)
//	SubjectKind  exact match on the subject's entity kind (person, team, ...)
//	SubjectName  exact match on the subject's name
//	SubjectID    exact match on the subject's canonical ID
//	Field        exact match on the record's sub-field
//	Status       active (the default) or any; expired filters non-usable
//	StaleAt      when set, keeps only records whose freshness is stale at t
//	FreshnessAt  when set, evaluates the freshness policy against t and
//	             keeps only records in the requested status (FreshStatus)
//	Limit        maximum number of records to return (0 = no limit)
type Query struct {
	Kind        memory.MemoryKind
	Scope       memory.MemoryScope
	SubjectKind string
	SubjectName string
	SubjectID   string
	Field       string

	// Status selects the invalidation lifecycle of the results:
	// StatusActive (zero value) returns only records that are currently
	// active; StatusAny returns records in every lifecycle state.
	Status QueryStatus

	// StaleAt, when non-zero, keeps only records that are stale at that time
	// (per the record's Freshness window). Stale records are typically the
	// input for a re-verification pass.
	StaleAt time.Time

	// FreshnessAt, when non-zero, evaluates the freshness policy against
	// that time and keeps only records in the FreshStatus. Records the
	// policy considers expired never satisfy FreshStatus.
	FreshStatus memory.FreshnessStatus
	FreshnessAt time.Time

	// Limit bounds the result size (0 = unlimited). Results are ordered by
	// stable ID ascending.
	Limit int
}

// QueryStatus selects the invalidation lifecycle of query results.
type QueryStatus string

const (
	// StatusActive returns only records in the active state (the default).
	StatusActive QueryStatus = ""

	// StatusAny returns records in every lifecycle state (active, superseded,
	// invalidated), including expired records.
	StatusAny QueryStatus = "any"
)

// NotFoundError is returned when a record does not exist.
type NotFoundError struct {
	ID memory.MemoryStableID
}

func (e NotFoundError) Error() string {
	return "memory record not found: " + string(e.ID)
}

// IsNotFound checks whether err is a NotFoundError.
func IsNotFound(err error) bool {
	var e NotFoundError
	return errors.As(err, &e)
}

// AlreadyExistsError is returned by Create when the record exists.
type AlreadyExistsError struct {
	ID memory.MemoryStableID
}

func (e AlreadyExistsError) Error() string {
	return "memory record already exists: " + string(e.ID)
}

// IsAlreadyExists checks whether err is an AlreadyExistsError.
func IsAlreadyExists(err error) bool {
	var e AlreadyExistsError
	return errors.As(err, &e)
}

// ErrSecretDetected is the base error for records that fail the
// sanitization gate and are therefore refused for persistence.
var ErrSecretDetected = errors.New("refusing to persist memory record: detected secret")

// SecretDetectedError is returned when a record's serialized form still
// contains a detected secret after sanitization.
//
// The error never includes the secret itself: Field names the record region
// that was refused (e.g. "value") and Count is the number of redaction
// operations that were applied before the gate failed.
type SecretDetectedError struct {
	// ID is the record that was refused (empty if the ID could not be read).
	ID memory.MemoryStableID
	// Field names the record region that tripped the gate.
	Field string
	// Count is the number of replacements performed before the gate failed.
	Count int
}

func (e SecretDetectedError) Error() string {
	return "refusing to persist memory record: detected secret in " + e.Field +
		" after " + itoa(e.Count) + " redaction(s)"
}

// IsSecretDetected checks whether err is a SecretDetectedError.
func IsSecretDetected(err error) bool {
	var e SecretDetectedError
	return errors.As(err, &e)
}

// SupersedeError is returned when a record cannot be superseded (it is not in
// the active state, or the new ID is empty).
type SupersedeError struct {
	ID     memory.MemoryStableID
	Reason string
}

func (e SupersedeError) Error() string {
	return "cannot supersede memory record " + string(e.ID) + ": " + e.Reason
}

// IsSupersedeError checks whether err is a SupersedeError.
func IsSupersedeError(err error) bool {
	var e SupersedeError
	return errors.As(err, &e)
}

// InvalidateError is returned when a record cannot be invalidated (it is not
// in the active state).
type InvalidateError struct {
	ID     memory.MemoryStableID
	Reason string
}

func (e InvalidateError) Error() string {
	return "cannot invalidate memory record " + string(e.ID) + ": " + e.Reason
}

// IsInvalidateError checks whether err is an InvalidateError.
func IsInvalidateError(err error) bool {
	var e InvalidateError
	return errors.As(err, &e)
}

// ErrInvalidRecord is returned when a record fails its invariants and is
// refused for persistence.
var ErrInvalidRecord = errors.New("refusing to persist memory record: invalid record")

// InvalidRecordError wraps a record validation error.
type InvalidRecordError struct {
	ID  memory.MemoryStableID
	Err error
}

func (e InvalidRecordError) Error() string {
	return "invalid memory record " + string(e.ID) + ": " + e.Err.Error()
}

func (e InvalidRecordError) Unwrap() error { return e.Err }

// IsInvalidRecord checks whether err is an InvalidRecordError.
func IsInvalidRecord(err error) bool {
	var e InvalidRecordError
	return errors.As(err, &e)
}

// itoa is a tiny integer formatter so error strings never expose helper
// dependencies. (strconv would be fine; this keeps the error path minimal.)
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
