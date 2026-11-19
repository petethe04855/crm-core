package sales

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEveryOfficialProvinceNormalizesToItself(t *testing.T) {
	all := AllProvinces()
	require.Len(t, all, 77, "Thailand has 77 provinces including Bangkok")
	for _, p := range all {
		require.Equal(t, p, NormalizeProvince(p, ""), "official name must be stable")
		require.NotEqual(t, RegionUnknown, RegionOf(p), "every province belongs to a region")
	}
}

func TestNormalizeProvinceFallbacks(t *testing.T) {
	bkk := NormalizeProvince("Bangkok", "")
	require.NotEqual(t, UnknownProvince, bkk)
	require.Equal(t, bkk, NormalizeProvince("BKK", ""))
	require.Equal(t, bkk, NormalizeProvince("", "10110"), "postal code fallback")

	require.Equal(t, UnknownProvince, NormalizeProvince("", ""))
	require.Equal(t, UnknownProvince, NormalizeProvince("Atlantis", "99999"))
}

func TestRegionsCoverEveryProvince(t *testing.T) {
	seen := map[string]struct{}{}
	for _, r := range Regions() {
		seen[r] = struct{}{}
	}
	require.NotContains(t, seen, RegionUnknown, "unknown is not a display region")
	require.Len(t, seen, 7)
}
