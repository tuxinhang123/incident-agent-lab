package agent

import "time"

// nowFn/timeSinceMS are package-level indirections so timing-related code
// stays deterministic in tests if needed.
var nowFn = time.Now

func timeSinceMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
