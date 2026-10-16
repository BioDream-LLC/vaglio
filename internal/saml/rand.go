package saml

import "crypto/rand"

// randRead wraps crypto/rand.Read.
func randRead(b []byte) (int, error) {
	return rand.Read(b)
}
