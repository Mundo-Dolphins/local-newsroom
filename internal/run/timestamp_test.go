package run

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewTimestampNormalizesToUTC(t *testing.T) {
	// A local time with an explicit zone must be normalized to UTC.
	local := time.Date(2024, 1, 15, 14, 30, 0, 500_000_000, time.FixedZone("EST", -5*3600))
	ts := NewTimestamp(local)
	want := time.Date(2024, 1, 15, 19, 30, 0, 500_000_000, time.UTC)
	if !ts.Time().Equal(want) {
		t.Errorf("Time() = %v, want %v", ts.Time(), want)
	}
	if ts.Time().Location() != time.UTC {
		t.Errorf("location = %v, want UTC", ts.Time().Location())
	}
	if got := ts.String(); got != "2024-01-15T19:30:00.5Z" {
		t.Errorf("String() = %q, want %q", got, "2024-01-15T19:30:00.5Z")
	}
}

func TestTimestampSubSecondPrecision(t *testing.T) {
	// Sub-second precision is preserved, with trailing zero fractions trimmed
	// by the RFC 3339 representation.
	ts := NewTimestamp(time.Date(2024, 1, 15, 12, 0, 0, 123_123_456, time.UTC))
	if got := ts.String(); got != "2024-01-15T12:00:00.123123456Z" {
		t.Errorf("got %q", got)
	}
	ts = NewTimestamp(time.Date(2024, 1, 15, 12, 0, 0, 123_900_000, time.UTC))
	if got := ts.String(); got != "2024-01-15T12:00:00.1239Z" {
		t.Errorf("got %q", got)
	}
}

func TestTimestampZeroIsNull(t *testing.T) {
	var zero Timestamp
	b, err := json.Marshal(&zero)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "null" {
		t.Errorf("marshaling a zero Timestamp pointer = %s, want null", b)
	}
}

func TestTimestampJSONRoundTrip(t *testing.T) {
	in := NewTimestamp(time.Date(2024, 1, 15, 19, 30, 0, 500_000_000, time.UTC))
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"2024-01-15T19:30:00.5Z"` {
		t.Fatalf("marshaled = %s", b)
	}
	var out Timestamp
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Equal(in) {
		t.Errorf("round-trip: %v != %v", out, in)
	}
	if out.Time().Location() != time.UTC {
		t.Errorf("round-trip location = %v, want UTC", out.Time().Location())
	}
}

func TestTimestampUnmarshalRejectsInvalid(t *testing.T) {
	cases := []string{`"2024-13-45T99:99:99Z"`, `"not-a-time"`, `"2024-01-15 12:00:00"`}
	for _, c := range cases {
		var ts Timestamp
		if err := json.Unmarshal([]byte(c), &ts); err == nil {
			t.Errorf("unmarshal(%s) succeeded, want error", c)
		}
	}
	// Explicit null on a pointer unmarshals to a nil pointer, not an error.
	var p *Timestamp
	if err := json.Unmarshal([]byte("null"), &p); err != nil {
		t.Errorf("unmarshal(null) = %v, want nil", err)
	}
	if p != nil {
		t.Errorf("unmarshal(null) should yield a nil timestamp, got %v", p)
	}
}

func TestTimestampBefore(t *testing.T) {
	a := NewTimestamp(at(10))
	b := NewTimestamp(at(20))
	if !a.Before(b) {
		t.Error("a.Before(b) = false, want true")
	}
	if b.Before(a) {
		t.Error("b.Before(a) = true, want false")
	}
	if a.Before(a) {
		t.Error("a.Before(a) = true, want false")
	}
}

func TestTimestampNumericFractionRoundTrip(t *testing.T) {
	// JSON input with a fractional second must parse correctly.
	var ts Timestamp
	if err := json.Unmarshal([]byte(`"2024-01-15T12:00:00.123Z"`), &ts); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 1, 15, 12, 0, 0, 123_000_000, time.UTC)
	if !ts.Time().Equal(want) {
		t.Errorf("parsed = %v, want %v", ts.Time(), want)
	}
}
