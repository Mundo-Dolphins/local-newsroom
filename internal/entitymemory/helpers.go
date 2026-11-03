package entitymemory

import (
	"time"
)

// formatWhen renders a timestamp for error messages; zero renders as
// "never".
func formatWhen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04:05 MST")
}
