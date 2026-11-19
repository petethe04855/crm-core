// Package timeutil is the CRM's single source of Thai business time. Every day
// and month boundary is cut in Asia/Bangkok (UTC+7), never in UTC, because the
// shops, the ERP reports and the staff all work on the Thai calendar.
package timeutil

import (
	"sync"
	"time"
)

// DateLayout is the wire and query format of a calendar day.
const DateLayout = "2006-01-02"

// iccOffset is UTC+7 in seconds: the fixed fallback zone when tzdata is absent.
const iccOffset = 7 * 60 * 60

var (
	bangkokOnce sync.Once
	bangkok     *time.Location
)

// Bangkok returns Asia/Bangkok. If the tz database cannot be loaded (slim
// container, Windows without zoneinfo) it falls back to a fixed UTC+7 zone, so
// callers never have to handle an error just to ask what time it is.
func Bangkok() *time.Location {
	bangkokOnce.Do(func() {
		loc, err := time.LoadLocation("Asia/Bangkok")
		if err != nil {
			loc = time.FixedZone("ICT", iccOffset)
		}
		bangkok = loc
	})
	return bangkok
}

// StartOfDay returns 00:00 of t's Bangkok day.
func StartOfDay(t time.Time) time.Time {
	local := t.In(Bangkok())
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, Bangkok())
}

// EndOfDay returns the exclusive end of t's Bangkok day, i.e. 00:00 of the next
// day. Use it as a `< end` bound so the whole day is included once.
func EndOfDay(t time.Time) time.Time {
	return StartOfDay(t).AddDate(0, 0, 1)
}

// MonthRange returns the half-open range [start, end) of a Bangkok month.
func MonthRange(year int, month time.Month) (start, end time.Time) {
	start = time.Date(year, month, 1, 0, 0, 0, 0, Bangkok())
	return start, start.AddDate(0, 1, 0)
}

// ParseDate parses "YYYY-MM-DD" as midnight in Bangkok.
func ParseDate(v string) (time.Time, error) {
	return time.ParseInLocation(DateLayout, v, Bangkok())
}
