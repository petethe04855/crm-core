package productcode

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

// Code format (plan CRM_LINE_OA_MEMBERSHIP.md §5.4, adjusted to the 3×4
// input boxes of the LIFF page): 11 random Crockford Base32 characters
// (55 bits from crypto/rand) + 1 checksum character = 12 characters, printed
// as XXXX-XXXX-XXXX. The alphabet has no I, L, O or U.
const (
	Alphabet     = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	RandomLength = 11
	Length       = RandomLength + 1
	SuffixLength = 4
)

// ErrInvalidFormat: not 12 Crockford characters, or the checksum is wrong.
var ErrInvalidFormat = errors.New("invalid product code format")

func value(c byte) int { return strings.IndexByte(Alphabet, c) }

// Checksum is sum(v_i × (2i+1)) mod 32 over the random part. Odd weights
// make every single-character mistake change the checksum.
func Checksum(random string) byte {
	sum := 0
	for i := 0; i < len(random); i++ {
		sum += value(random[i]) * (2*i + 1)
	}
	return Alphabet[sum%len(Alphabet)]
}

// Generate returns one new code (12 characters, no dashes).
func Generate(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	buf := make([]byte, RandomLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	out := make([]byte, RandomLength)
	for i, b := range buf {
		out[i] = Alphabet[int(b)&31] // 256 is a multiple of 32: no bias
	}
	random := string(out)
	return random + string(Checksum(random)), nil
}

// Normalize uppercases, drops spaces and dashes, maps the Crockford look-alikes
// (O→0, I/L→1) and checks length, alphabet and checksum.
func Normalize(input string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	code := b.String()
	if len(code) != Length {
		return "", ErrInvalidFormat
	}
	for i := 0; i < len(code); i++ {
		if value(code[i]) < 0 {
			return "", ErrInvalidFormat
		}
	}
	if Checksum(code[:RandomLength]) != code[RandomLength] {
		return "", ErrInvalidFormat
	}
	return code, nil
}

// Format prints a normalized code as XXXX-XXXX-XXXX.
func Format(code string) string {
	if len(code) != Length {
		return code
	}
	return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
}

// Hash is the stored HMAC-SHA256 of a normalized code.
func Hash(secret []byte, code string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

// Suffix is the last four characters, kept to find a code a member reports.
func Suffix(code string) string {
	if len(code) < SuffixLength {
		return code
	}
	return code[len(code)-SuffixLength:]
}
