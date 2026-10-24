// Package merkle is the Merkle tree of RFC 6962 (Certificate Transparency), section 2.1, as RFC 9162 restates it: the
// tree hash of a list of leaves, an inclusion proof that a leaf is in a tree of a given size, and a consistency proof
// that a larger tree extends a smaller one (every leaf of the smaller one unchanged, in place). Only SHA-256, from the
// standard library.
package merkle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Hash is a SHA-256 hash.
type Hash = [32]byte

// Leaf is the hash of a leaf's data: SHA-256(0x00 || data).
func Leaf(data []byte) Hash {
	return sha256.Sum256(append([]byte{0}, data...))
}

// node is the hash of an inner node: SHA-256(0x01 || left || right).
func node(l, r Hash) Hash {
	var b [65]byte
	b[0] = 1
	copy(b[1:], l[:])
	copy(b[33:], r[:])
	return sha256.Sum256(b[:])
}

// split is the largest power of two smaller than n (n > 1).
func split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

// Root is the tree hash of the leaf hashes: MTH of RFC 6962. The empty tree's is SHA-256 of nothing.
func Root(leaves []Hash) Hash {
	switch len(leaves) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return leaves[0]
	}
	k := split(len(leaves))
	return node(Root(leaves[:k]), Root(leaves[k:]))
}

// Inclusion is the audit path of leaf m in the tree of the leaves: PATH(m, D[n]).
func Inclusion(m int, leaves []Hash) ([]Hash, error) {
	if m < 0 || m >= len(leaves) {
		return nil, fmt.Errorf("merkle: leaf %d is not in a tree of %d", m, len(leaves))
	}
	return path(m, leaves), nil
}

func path(m int, d []Hash) []Hash {
	if len(d) <= 1 {
		return nil
	}
	k := split(len(d))
	if m < k {
		return append(path(m, d[:k]), Root(d[k:]))
	}
	return append(path(m-k, d[k:]), Root(d[:k]))
}

// Consistency is the proof that the tree of the first m leaves is a prefix of the tree of all of them: PROOF(m, D[n]).
func Consistency(m int, leaves []Hash) ([]Hash, error) {
	if m < 0 || m > len(leaves) {
		return nil, fmt.Errorf("merkle: no tree of %d inside one of %d", m, len(leaves))
	}
	if m == 0 || m == len(leaves) {
		return nil, nil
	}
	return subproof(m, leaves, true), nil
}

func subproof(m int, d []Hash, whole bool) []Hash {
	n := len(d)
	if m == n {
		if whole {
			return nil
		}
		return []Hash{Root(d)}
	}
	k := split(n)
	if m <= k {
		return append(subproof(m, d[:k], whole), Root(d[k:]))
	}
	return append(subproof(m-k, d[k:], false), Root(d[:k]))
}

// ErrProof is a proof that does not verify.
var ErrProof = errors.New("merkle: the proof does not verify")

// VerifyInclusion checks that leaf, at index m, is in the tree of size n with this root (RFC 9162, 2.1.3.2).
func VerifyInclusion(m, n int, leaf Hash, proof []Hash, root Hash) error {
	if m < 0 || m >= n {
		return ErrProof
	}
	fn, sn := m, n-1
	r := leaf
	for _, p := range proof {
		if sn == 0 {
			return ErrProof
		}
		if fn&1 == 1 || fn == sn {
			r = node(p, r)
			if fn&1 == 0 {
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			r = node(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 || !bytes.Equal(r[:], root[:]) {
		return ErrProof
	}
	return nil
}

// VerifyConsistency checks that the tree of size n with root2 extends the tree of size m with root1 (RFC 9162,
// 2.1.4.2).
func VerifyConsistency(m, n int, root1, root2 Hash, proof []Hash) error {
	switch {
	case m < 0 || m > n:
		return ErrProof
	case m == n:
		if len(proof) != 0 || root1 != root2 {
			return ErrProof
		}
		return nil
	case m == 0:
		if len(proof) != 0 {
			return ErrProof
		}
		return nil
	}
	if len(proof) == 0 {
		return ErrProof
	}
	// A size that is a power of two is its own root, the first node of the path.
	if m&(m-1) == 0 {
		proof = append([]Hash{root1}, proof...)
	}
	fn, sn := m-1, n-1
	for fn&1 == 1 {
		fn >>= 1
		sn >>= 1
	}
	fr, sr := proof[0], proof[0]
	for _, c := range proof[1:] {
		if sn == 0 {
			return ErrProof
		}
		if fn&1 == 1 || fn == sn {
			fr, sr = node(c, fr), node(c, sr)
			if fn&1 == 0 {
				for fn&1 == 0 && fn != 0 {
					fn >>= 1
					sn >>= 1
				}
			}
		} else {
			sr = node(sr, c)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 || fr != root1 || sr != root2 {
		return ErrProof
	}
	return nil
}
