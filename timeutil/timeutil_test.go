package timeutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBangkokIsUTCPlusSeven(t *testing.T) {
	_, offset := time.Now().In(Bangkok()).Zone()
	require.Equal(t, 7*60*60, offset)
}

func TestStartOfDayUsesTheThaiDayNotUTC(t *testing.T) {
	// 00:30 on 18 Nov in Bangkok is still 17:30 on 17 Nov in UTC; the CRM day
	// must stay the 18th.
	lateNight := time.Date(2026, 11, 18, 0, 30, 0, 0, Bangkok())
	start := StartOfDay(lateNight)
	require.Equal(t, 18, start.Day())
	require.Equal(t, 0, start.Hour())
	require.Equal(t, 0, start.Minute())
	require.Equal(t, lateNight.UTC().Day(), 17, "sanity: the UTC day is the previous one")

	// 23:59 on 31 Dec in Bangkok is 16:59 UTC on the same date; the day after
	// must be 1 Jan 2027 Thai time.
	endOfYear := time.Date(2026, 12, 31, 23, 59, 0, 0, Bangkok())
	next := EndOfDay(endOfYear)
	require.Equal(t, 2027, next.Year())
	require.Equal(t, time.January, next.Month())
	require.Equal(t, 1, next.Day())
}

func TestEndOfDayIsTheNextMidnight(t *testing.T) {
	d := time.Date(2026, 11, 30, 14, 5, 0, 0, Bangkok())
	end := EndOfDay(d)
	require.Equal(t, 1, end.Day())
	require.Equal(t, time.December, end.Month())
	require.Equal(t, 0, end.Hour())
	require.True(t, end.After(d))
	require.Equal(t, 24*time.Hour, end.Sub(StartOfDay(d)))
}

func TestMonthRangeCrossesYearBoundary(t *testing.T) {
	start, end := MonthRange(2026, time.December)
	require.Equal(t, 1, start.Day())
	require.Equal(t, 2026, start.Year())
	require.Equal(t, 2027, end.Year())
	require.Equal(t, time.January, end.Month())
	require.Equal(t, time.Date(2026, 12, 1, 0, 0, 0, 0, Bangkok()), start)
	require.Equal(t, 31*24*time.Hour, end.Sub(start))
}

func TestParseDateReadsThaiMidnight(t *testing.T) {
	got, err := ParseDate("2026-11-17")
	require.NoError(t, err)
	require.Equal(t, 2026, got.Year())
	require.Equal(t, time.November, got.Month())
	require.Equal(t, 17, got.Day())
	require.Equal(t, 0, got.Hour())
	_, offset := got.Zone()
	require.Equal(t, 7*60*60, offset)

	_, err = ParseDate("17/11/2026")
	require.Error(t, err)
}
