// Package businessclock defines the civil-time policy used by database-backed
// business dates. Keep ordinary duration/deadline clocks in the standard time
// package; do not mutate process-wide time.Local.
package businessclock

import (
	"time"
	_ "time/tzdata" // keep DSN location loading independent of the host image
)

const UTCOffset = "+08:00"

// IANA Etc/GMT-8 is fixed UTC+08:00; a regional name would reintroduce
// historical daylight-saving offsets when the MySQL driver parses its DSN.
var location = time.FixedZone("Etc/GMT-8", 8*60*60)

func Location() *time.Location { return location }

// Now is for civil-date formatting. Keep duration and deadline measurement on
// the standard time.Now clock.
func Now() time.Time { return time.Now().In(location) }
