package review

import (
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// jsonUnmarshal parses JSON. Kept as a package-level function variable so
// tests can substitute a parser without touching the build path.
var jsonUnmarshal = json.Unmarshal

// osStat is the filesystem stat indirection used by the bundle (tests can
// substitute it to fake file presence/absence).
var osStat = os.Stat

// duration renders a span as a fixed-format, human-readable string
// (deterministic: no sub-second noise, no locale).
func duration(start, end time.Time) string {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return ""
	}
	d := end.Sub(start)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return strconv.Itoa(h) + "h " + strconv.Itoa(m) + "m"
	case m > 0:
		return strconv.Itoa(m) + "m " + strconv.Itoa(s) + "s"
	default:
		return strconv.Itoa(s) + "s"
	}
}
