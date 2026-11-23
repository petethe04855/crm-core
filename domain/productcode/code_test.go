package productcode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateFormatAndNormalize(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := Generate(nil)
		require.NoError(t, err)
		require.Len(t, code, Length)
		for _, c := range code {
			require.True(t, strings.ContainsRune(Alphabet, c), code)
		}
		printed := Format(code)
		require.Regexp(t, `^[0-9A-Z]{4}-[0-9A-Z]{4}-[0-9A-Z]{4}$`, printed)

		back, err := Normalize(" " + strings.ToLower(printed) + " ")
		require.NoError(t, err)
		require.Equal(t, code, back)
	}
}

func TestChecksumCatchesEverySingleTypo(t *testing.T) {
	code, err := Generate(nil)
	require.NoError(t, err)
	for pos := 0; pos < Length; pos++ {
		for _, c := range Alphabet {
			if byte(c) == code[pos] {
				continue
			}
			typo := code[:pos] + string(c) + code[pos+1:]
			_, err := Normalize(typo)
			require.ErrorIs(t, err, ErrInvalidFormat, "typo at %d: %s", pos, typo)
		}
	}
}

func TestNormalizeMapsLookAlikesAndRejectsJunk(t *testing.T) {
	// Build a code containing 0 and 1 so O/I/L map back to it.
	random := "01ABCDEFGH0"
	code := random + string(Checksum(random))
	got, err := Normalize("OIAB-CDEF-GHO" + code[11:])
	require.NoError(t, err)
	require.Equal(t, code, got)
	got, err = Normalize("0LAB-CDEF-GH0" + code[11:])
	require.NoError(t, err)
	require.Equal(t, code, got)

	for _, bad := range []string{"", "ABCD", "ABCD-EFGH-JKMN-P", "UUUU-UUUU-UUUU", "ABCD-EFGH-JK*N"} {
		_, err := Normalize(bad)
		require.ErrorIs(t, err, ErrInvalidFormat, bad)
	}
}

func TestHashAndSuffix(t *testing.T) {
	require.Len(t, Hash([]byte("s"), "ABCDEFGHJKMN"), 64)
	require.NotEqual(t, Hash([]byte("a"), "ABCDEFGHJKMN"), Hash([]byte("b"), "ABCDEFGHJKMN"))
	require.Equal(t, "JKMN", Suffix("ABCDEFGHJKMN"))
}
