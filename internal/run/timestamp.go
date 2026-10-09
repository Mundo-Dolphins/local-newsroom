package run

import (
	"encoding/json"
	"fmt"
	"time"
)

// Timestamp is the time representation used throughout the run manifest.
//
// Every timestamp is normalized to UTC and stripped of any monotonic clock
// reading, so manifests are stable, comparable, and byte-identical across
// platforms and process restarts.
//
// Wire format (JSON):
//   - A set timestamp marshals as an RFC 3339 string, e.g. "2024-01-15T12:00:00Z".
//     Fractional seconds are preserved when non-zero.
//   - A zero timestamp marshals as JSON null (i.e. "not set").
type Timestamp time.Time

// NewTimestamp converts a time.Time into a manifest Timestamp in UTC.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp(t.UTC().Round(0))
}

// Now returns the current time as a manifest Timestamp.
func Now() Timestamp {
	return NewTimestamp(time.Now())
}

// Time returns the underlying time.Time (UTC, no monotonic reading).
func (t Timestamp) Time() time.Time {
	return time.Time(t)
}

// IsZero reports whether the timestamp is unset.
func (t Timestamp) IsZero() bool {
	return time.Time(t).IsZero()
}

// Equal reports whether t and other represent the same instant.
func (t Timestamp) Equal(other Timestamp) bool {
	return time.Time(t).Equal(time.Time(other))
}

// Before reports whether t is strictly earlier than other.
func (t Timestamp) Before(other Timestamp) bool {
	return time.Time(t).Before(time.Time(other))
}

// String renders the timestamp as an RFC 3339 string, or the empty string
// when unset.
func (t Timestamp) String() string {
	if t.IsZero() {
		return ""
	}
	return time.Time(t).Format(time.RFC3339Nano)
}

// MarshalJSON implements json.Marshaler.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time().Format(time.RFC3339Nano))
}

// UnmarshalJSON implements json.Unmarshaler.
//
// It accepts JSON null (producing a zero Timestamp) or an RFC 3339 string in
// any UTC offset; the parsed value is normalized to UTC.
func (t *Timestamp) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("invalid timestamp %s: %w", string(data), err)
	}
	if raw == nil {
		*t = Timestamp{}
		return nil
	}
	str, ok := raw.(string)
	if !ok {
		return fmt.Errorf("invalid timestamp %s: expected string or null", string(data))
	}
	if str == "" {
		*t = Timestamp{}
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, str)
	if err != nil {
		return fmt.Errorf("invalid timestamp %q: %w", str, err)
	}
	*t = NewTimestamp(parsed)
	return nil
}
