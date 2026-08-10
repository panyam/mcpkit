package files

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashLen is the number of hex characters in a Hash, and so the width of the
// precondition a caller echoes back on Edit.ExpectHash.
const HashLen = 12

// Hash fingerprints file content for staleness detection.
//
// It is a truncated sha256: 12 hex characters, 48 bits. The truncation is
// deliberate. A model has to carry this value from a read to a later edit, and
// a full 64-character digest costs tokens on every hop to answer a question
// that only needs "is this the same content I was shown".
//
// 48 bits is not a security boundary and is not trying to be. The threat it
// addresses is an accidental change: a formatter ran, a sibling tool wrote the
// file, the user saved in their editor. Anyone positioned to search for a
// collision already has write access to the file and can simply write what
// they want, so a wider digest would buy nothing.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:HashLen]
}
