package promotion

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bangkok() *time.Location { return time.FixedZone("ICT", 7*60*60) }

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, bangkok())
}

func TestDisplayStatusBoundaries(t *testing.T) {
	p := Promotion{
		Status:         StatusPublished,
		DisplayStartAt: at(2026, time.November, 18, 10, 0),
		DisplayEndAt:   at(2026, time.November, 18, 20, 0),
	}

	require.Equal(t, DisplayScheduled, DisplayStatus(p, at(2026, time.November, 18, 9, 59)))
	require.Equal(t, DisplayLive, DisplayStatus(p, at(2026, time.November, 18, 10, 0)),
		"the start minute is live")
	require.Equal(t, DisplayLive, DisplayStatus(p, at(2026, time.November, 18, 19, 59)))
	require.Equal(t, DisplayExpired, DisplayStatus(p, at(2026, time.November, 18, 20, 0)),
		"the end minute is already expired")

	p.Status = StatusDraft
	require.Equal(t, DisplayDraft, DisplayStatus(p, at(2026, time.November, 18, 12, 0)))
	p.Status = StatusArchived
	require.Equal(t, DisplayArchived, DisplayStatus(p, at(2026, time.November, 18, 12, 0)))
}

func TestIsLiveAt(t *testing.T) {
	p := Promotion{
		Status:         StatusPublished,
		DisplayStartAt: at(2026, time.November, 18, 10, 0),
		DisplayEndAt:   at(2026, time.November, 18, 20, 0),
	}
	require.True(t, IsLiveAt(p, at(2026, time.November, 18, 10, 0)))
	require.True(t, IsLiveAt(p, at(2026, time.November, 18, 19, 59)))
	require.False(t, IsLiveAt(p, at(2026, time.November, 18, 20, 0)))
	require.False(t, IsLiveAt(p, at(2026, time.November, 18, 9, 59)))
	p.Status = StatusDraft
	require.False(t, IsLiveAt(p, at(2026, time.November, 18, 12, 0)))
}

func TestAudienceMatches(t *testing.T) {
	now := at(2026, time.November, 18, 12, 0)
	gold := MemberFacts{TierCode: "gold", RegisteredAt: at(2026, time.September, 1, 9, 0)}
	newMember := MemberFacts{TierCode: "bronze", RegisteredAt: at(2026, time.November, 15, 9, 0)}

	require.True(t, Audience{}.Matches(newMember, now), "empty audience is everyone")

	tierOnly := Audience{Tiers: []string{"gold"}}
	require.True(t, tierOnly.Matches(gold, now))
	require.False(t, tierOnly.Matches(newMember, now))

	days := 30
	newOnly := Audience{NewMemberDays: &days}
	require.True(t, newOnly.Matches(newMember, now))
	require.False(t, newOnly.Matches(gold, now), "an old member is not new")

	both := Audience{Tiers: []string{"gold"}, NewMemberDays: &days}
	require.False(t, both.Matches(gold, now), "gold but not new")
	require.False(t, both.Matches(newMember, now), "new but not gold")
	bothMember := MemberFacts{TierCode: "gold", RegisteredAt: at(2026, time.November, 10, 9, 0)}
	require.True(t, both.Matches(bothMember, now), "both conditions must hold")

	zero := 0
	require.True(t, Audience{NewMemberDays: &zero}.Matches(MemberFacts{RegisteredAt: now}, now))
	require.False(t, Audience{NewMemberDays: &zero}.Matches(MemberFacts{RegisteredAt: now.Add(-time.Minute)}, now))
}

func TestAudienceRegistrationExactlyNDaysAgo(t *testing.T) {
	days := 7
	now := at(2026, time.November, 18, 12, 0)
	require.True(t, Audience{NewMemberDays: &days}.Matches(
		MemberFacts{RegisteredAt: now.Add(-7 * 24 * time.Hour)}, now))
	require.False(t, Audience{NewMemberDays: &days}.Matches(
		MemberFacts{RegisteredAt: now.Add(-7*24*time.Hour - time.Second)}, now))
}
