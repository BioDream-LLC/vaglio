package merkle

import (
	"encoding/hex"
	"testing"
)

// The RFC 6962 reference leaves and the tree hashes of their first 1 to 8 (Certificate Transparency's test data).
var refLeaves = []string{"", "00", "10", "2021", "3031", "40414243", "5051525354555657", "606162636465666768696a6b6c6d6e6f"}

var refRoots = []string{
	"6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d",
	"fac54203e7cc696cf0dfcb42c92a1d9dbaf70ad9e621f4bd8d98662f00e3c125",
	"aeb6bcfe274b70a14fb067a5e5578264db0fa9b51af5e0ba159158f329e06e77",
	"d37ee418976dd95753c1c73862b9398fa2a2cf9b4ff0fdfe8b30cd95209614b7",
	"4e3bbb1f7b478dcfe71fb631631519a3bca12c9aefca1612bfce4c13a86264d4",
	"76e67dadbcdf1e10e1b74ddc608abd2f98dfb16fbce75277b5232a127f2087ef",
	"ddb89be403809e325750d3d263cd78929c2942b7942a34b77e122c9594a74c8c",
	"5dc9da79a70659a9ad559cb701ded9a2ab9d823aad2f4960cfe370eff4604328",
}

func leaves(n int) []Hash {
	var out []Hash
	for i := 0; i < n; i++ {
		var d []byte
		if i < len(refLeaves) {
			d, _ = hex.DecodeString(refLeaves[i])
		} else {
			d = []byte{byte(i), byte(i >> 8)}
		}
		out = append(out, Leaf(d))
	}
	return out
}

func TestReferenceRoots(t *testing.T) {
	for n := 1; n <= 8; n++ {
		r := Root(leaves(n))
		if got := hex.EncodeToString(r[:]); got != refRoots[n-1] {
			t.Errorf("size %d: %s", n, got)
		}
	}
}

// TestProofs: every inclusion and consistency proof in trees up to 40 leaves verifies, and fails against the wrong
// root, the wrong leaf or a changed leaf.
func TestProofs(t *testing.T) {
	all := leaves(40)
	for n := 1; n <= len(all); n++ {
		d := all[:n]
		root := Root(d)
		for m := 0; m < n; m++ {
			p, err := Inclusion(m, d)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyInclusion(m, n, d[m], p, root); err != nil {
				t.Fatalf("inclusion %d of %d: %v", m, n, err)
			}
			if n > 1 && VerifyInclusion(m, n, d[(m+1)%n], p, root) == nil {
				t.Fatalf("inclusion %d of %d verified the wrong leaf", m, n)
			}
		}
		for m := 1; m <= n; m++ {
			p, err := Consistency(m, d)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyConsistency(m, n, Root(d[:m]), root, p); err != nil {
				t.Fatalf("consistency %d to %d: %v", m, n, err)
			}
			if m < n {
				changed := append([]Hash(nil), d...)
				changed[m-1] = Leaf([]byte("rewritten"))
				if VerifyConsistency(m, n, Root(d[:m]), Root(changed), p) == nil {
					t.Fatalf("consistency %d to %d verified a rewritten tree", m, n)
				}
			}
		}
	}
}
