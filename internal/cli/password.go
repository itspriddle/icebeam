package cli

import (
	"crypto/rand"
	"fmt"
	"io"
)

// passwordCharset is the documented alphabet a generated repository password is
// drawn from: the 62 alphanumerics plus "-" and "_". Its length (64) divides 256
// evenly, so a uniformly random byte maps to a character with no modulo bias —
// every character is equally likely. The set avoids shell-quoting hazards and
// visually ambiguous punctuation while staying URL/credential-file safe.
const passwordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// generatedPasswordLength is the number of characters in a generated repository
// password. At 32 characters drawn from a 64-symbol alphabet that is 192 bits of
// entropy, comfortably above the PRD's ">= 24 random characters" floor.
const generatedPasswordLength = 32

// randReader is the source of randomness for password generation. It is a
// package variable so a test can substitute a deterministic reader and assert an
// exact generated value; production uses the crypto/rand CSPRNG.
var randReader io.Reader = rand.Reader

// generatePassword returns a strong repository password: generatedPasswordLength
// characters drawn uniformly from passwordCharset using the CSPRNG (randReader).
// Because len(passwordCharset) is 64 — a divisor of 256 — each random byte maps
// to a character with no modulo bias, so no rejection sampling is needed. The
// returned value is the repository password used for the probe/init and stored
// like any entered password; it is never written to config or logs.
func generatePassword() (string, error) {
	buf := make([]byte, generatedPasswordLength)
	if _, err := io.ReadFull(randReader, buf); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	for i, b := range buf {
		buf[i] = passwordCharset[int(b)%len(passwordCharset)]
	}
	return string(buf), nil
}
