package saml

import "testing"

// FuzzEntraResponse: a real signed Entra response, changed anywhere, is refused or still names the same person. A
// change can never sign someone else in.
func FuzzEntraResponse(f *testing.F) {
	f.Add(0, byte(0))
	f.Add(100, byte('x'))
	f.Fuzz(func(t *testing.T, at int, b byte) {
		resetReplayCache()
		sp := entraServiceProvider(t)
		orig := realEntraResponse(t)
		want, err := sp.parseResponseBytes(orig, whenEntraSignedIt, func(id string) bool { return id == entraRequestID })
		if err != nil {
			t.Fatal(err)
		}
		if at < 0 {
			at = -at
		}
		raw := append([]byte(nil), orig...)
		raw[at%len(raw)] = b
		resetReplayCache()
		got, err := sp.parseResponseBytes(raw, whenEntraSignedIt, func(id string) bool { return id == entraRequestID })
		if err != nil {
			return
		}
		if got.NameID != want.NameID || len(got.Attributes) != len(want.Attributes) {
			t.Fatalf("a changed response signed in %q (%d attributes), not %q", got.NameID, len(got.Attributes), want.NameID)
		}
		for k, v := range want.Attributes {
			g := got.Attributes[k]
			if len(g) != len(v) {
				t.Fatalf("attribute %s changed: %v, not %v", k, g, v)
			}
			for i := range v {
				if g[i] != v[i] {
					t.Fatalf("attribute %s changed: %v, not %v", k, g, v)
				}
			}
		}
	})
}
