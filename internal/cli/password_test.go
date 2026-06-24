package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withRandReader swaps the package randomness source for the given reader and
// restores it when the test ends, so a test can assert an exact generated value.
func withRandReader(t *testing.T, r io.Reader) {
	t.Helper()
	orig := randReader
	randReader = r
	t.Cleanup(func() { randReader = orig })
}

func TestGeneratePasswordLengthAndCharset(t *testing.T) {
	pw, err := generatePassword()
	require.NoError(t, err)

	assert.Len(t, pw, generatedPasswordLength)
	assert.GreaterOrEqual(t, len(pw), 24, "generated password must be at least 24 characters")
	for _, r := range pw {
		assert.True(t, strings.ContainsRune(passwordCharset, r), "character %q not in documented charset", r)
	}
}

func TestGeneratePasswordIsDeterministicWithSeededReader(t *testing.T) {
	// A reader of all-zero bytes maps every position to charset index 0.
	withRandReader(t, bytes.NewReader(make([]byte, generatedPasswordLength)))

	pw, err := generatePassword()
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat(string(passwordCharset[0]), generatedPasswordLength), pw)
}

func TestGeneratePasswordMapsBytesUnbiased(t *testing.T) {
	// Bytes 0..length-1 map to charset[i % 64]; since the charset has 64 entries
	// and the length is 32, each byte selects its own character directly.
	src := make([]byte, generatedPasswordLength)
	for i := range src {
		src[i] = byte(i)
	}
	withRandReader(t, bytes.NewReader(src))

	pw, err := generatePassword()
	require.NoError(t, err)

	want := make([]byte, generatedPasswordLength)
	for i := range want {
		want[i] = passwordCharset[i%len(passwordCharset)]
	}
	assert.Equal(t, string(want), pw)
}

func TestGeneratePasswordSurfacesReaderError(t *testing.T) {
	// A short reader cannot fill the buffer, so io.ReadFull errors and the failure
	// is surfaced rather than yielding a weak partial password.
	withRandReader(t, bytes.NewReader([]byte{0x01}))

	_, err := generatePassword()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate password")
}
