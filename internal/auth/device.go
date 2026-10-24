package auth

// Known browsers: the answer to the lockout gap. Five wrong passwords for a name lock that name for 15 minutes, which
// is what stops guessing, but it also let anyone who knew a name lock its owner out. A browser that has signed in as
// a name before carries a device cookie for it (signed by this server, for that name only); its attempts are counted
// on their own, so a stranger's failures lock the name everywhere except the browsers its owner already uses. A device
// cookie proves nothing about the password: the password is still checked on every sign-in.
//
// This is the "device cookies" defence OWASP describes for exactly this problem.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/vaglio/internal/fsx"
)

// DeviceLife is how long a device cookie is honoured after the sign-in that issued it.
const DeviceLife = 90 * 24 * time.Hour

// DeviceKey reads the key device cookies are signed with from dir/device.key, making it the first time. Kept on disk so
// known browsers stay known across restarts.
func DeviceKey(dir string) ([]byte, error) {
	p := filepath.Join(dir, "device.key")
	if b, err := os.ReadFile(p); err == nil {
		if len(b) != 32 {
			return nil, errors.New(p + " is not a 32-byte key")
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsx.Replace(p+".new", p)
	}
	if err == nil {
		err = fsx.SyncDir(dir)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// SetDeviceKey replaces the key device cookies are signed with (NewSessions starts with a random one, valid until the
// process ends).
func (s *Sessions) SetDeviceKey(k []byte) {
	s.mu.Lock()
	s.deviceKey = append([]byte(nil), k...)
	s.mu.Unlock()
}

func (s *Sessions) deviceMAC(name, rest string) string {
	m := hmac.New(sha256.New, s.deviceKey)
	m.Write([]byte("vaglio device cookie\x00" + name + "\x00" + rest))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newDevice issues a device cookie value for name: nonce.issued.mac (the name is not in it; it is checked against the
// name being signed in).
func (s *Sessions) newDevice(name string, now time.Time) string {
	n := make([]byte, 12)
	_, _ = rand.Read(n)
	rest := base64.RawURLEncoding.EncodeToString(n) + "." + strconv.FormatInt(now.Unix(), 10)
	return rest + "." + s.deviceMAC(name, rest)
}

// device returns the identity of a valid device cookie for name ("" when the cookie is missing, forged, for another
// name or too old).
func (s *Sessions) device(name, cookie string, now time.Time) string {
	i := strings.LastIndexByte(cookie, '.')
	if i < 0 || name == "" {
		return ""
	}
	rest, mac := cookie[:i], cookie[i+1:]
	parts := strings.Split(rest, ".")
	if len(parts) != 2 {
		return ""
	}
	issued, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ""
	}
	if age := now.Sub(time.Unix(issued, 0)); age < -time.Minute || age > DeviceLife {
		return ""
	}
	if !hmac.Equal([]byte(mac), []byte(s.deviceMAC(name, rest))) {
		return ""
	}
	return name + "\x00" + parts[0]
}

// KnownDevice is whether cookie is a valid device cookie for name.
func (s *Sessions) KnownDevice(name, cookie string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device(name, cookie, s.now()) != ""
}
