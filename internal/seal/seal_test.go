package seal

import (
	"bytes"
	"errors"
	"testing"
)

const testKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 0..31

func TestSeal(t *testing.T) {
	SetDefault(nil)
	if got := Blob("bundle", []byte("x")); string(got) != "x" {
		t.Errorf("no key: %q", got)
	}
	k, err := Parse(testKey)
	if err != nil {
		t.Fatal(err)
	}
	SetDefault(k)
	defer SetDefault(nil)
	b := Blob("bundle", []byte(`{"patient":"p"}`))
	if !Sealed(b) || bytes.Contains(b, []byte("patient")) {
		t.Fatalf("not sealed: %q", b)
	}
	if out, err := Unblob("bundle", b); err != nil || string(out) != `{"patient":"p"}` {
		t.Errorf("%q %v", out, err)
	}
	if _, err := Unblob("fax", b); err == nil {
		t.Error("opened for another purpose")
	}
	b[len(b)-1] ^= 1
	if _, err := Unblob("bundle", b); err == nil {
		t.Error("a changed blob opened")
	}
	l := Line([]byte(`{"seq":1}`))
	if out, err := Unline(l); err != nil || string(out) != `{"seq":1}` || bytes.ContainsAny(l, "\n") {
		t.Errorf("%q %v", out, err)
	}
	if out, _ := Unline([]byte(`{"seq":2}`)); string(out) != `{"seq":2}` {
		t.Error("a plain line was not read")
	}
	other, _ := Parse("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1eff")
	SetDefault(other)
	if _, err := Unline(l); err == nil {
		t.Error("opened with the wrong key")
	}
	SetDefault(nil)
	if _, err := Unline(l); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	for _, bad := range []string{"", "short", "AAAA"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
