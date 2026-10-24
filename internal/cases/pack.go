package cases

// Pack files: where request Bundles are kept since 2026-10-24. Before, each Bundle was a file of its own, written,
// synced, renamed into place and its directory synced, two disk syncs per request that no other request could share,
// and on a Mac's disk those bounded a server at about 150 requests a second. Now Bundles are appended to a pack file
// and synced together, the way the audit log commits: requests arriving together share one sync. A Bundle is still on
// disk, synced, before the record that names it is written.
//
// A pack is bundles/pack-NNNNNN.vgp, a run of entries:
//
//	VGB1 <sha-256 of the Bundle, hex> <length of what follows>\n<the Bundle, sealed when a data key is set>\n
//
// Content-addressed, so nothing else is needed to check an entry: what is read back must hash to its name. A power
// cut can leave the last entry short; it is cut off when the server next writes (its record was never written, since
// the record waits for the sync). A pack grows to packLimit and the next one is started. Older single files
// (bundles/<sha>.json) are still read.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/biodream-llc/vaglio/internal/fsx"
	"github.com/biodream-llc/vaglio/internal/seal"
)

// packLimit is the size a pack is closed at.
var packLimit int64 = 256 << 20

const packMagic = "VGB1 "

type packLoc struct {
	seg         int
	off, length int64 // the payload's place
	end         int64 // where the entry ends: durable once the pack is synced this far
}

// packs is a directory's pack files.
type packs struct {
	dir string

	mu       sync.Mutex
	cond     *sync.Cond
	index    map[string]packLoc
	scanned  map[int]int64 // how far each pack has been read into the index
	cur      *os.File      // the pack being written, nil until the first write
	seg      int
	size     int64 // its length
	synced   int64 // how much of it is on disk for certain
	syncing  bool
	writable bool   // the torn tail, if any, was cut off
	single   string // a lone pack file being checked (openPacksAt), instead of the directory's
	legacy   bool   // the directory holds Bundles kept as single files (before packs): look for them too
}

func openPacks(dir string) *packs {
	p := &packs{dir: dir, index: map[string]packLoc{}, scanned: map[int]int64{}}
	p.cond = sync.NewCond(&p.mu)
	// Looking for an old single file on every request costs a stat, and a stat waits while the disk syncs: only
	// where some exist.
	old, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	p.legacy = len(old) > 0
	p.mu.Lock()
	p.rescan()
	p.mu.Unlock()
	return p
}

func packName(dir string, seg int) string {
	return filepath.Join(dir, fmt.Sprintf("pack-%06d.vgp", seg))
}

// segments lists the packs present, oldest first.
func segments(dir string) []int {
	names, _ := filepath.Glob(filepath.Join(dir, "pack-*.vgp"))
	var out []int
	for _, n := range names {
		if s, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "pack-"), ".vgp")); err == nil {
			out = append(out, s)
		}
	}
	sort.Ints(out)
	return out
}

// rescan reads what has been appended to the packs since the last look (another process, a standby, may write them).
// It returns, for the last pack, where its last whole entry ends. p.mu is held.
func (p *packs) rescan() int64 {
	var good int64
	for _, seg := range segments(p.dir) {
		from := p.scanned[seg]
		f, err := os.Open(packName(p.dir, seg))
		if err != nil {
			continue
		}
		end := scanPack(f, from, func(sum string, off, length, end int64) {
			if _, ok := p.index[sum]; !ok {
				p.index[sum] = packLoc{seg: seg, off: off, length: length, end: end}
			}
		})
		f.Close()
		p.scanned[seg] = end
		good = end
	}
	return good
}

// scanPack reads entries from off, calling each for every whole one, and returns where the last whole one ends.
func scanPack(f *os.File, off int64, each func(sum string, payloadOff, length, end int64)) int64 {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off
	}
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		head, err := r.ReadString('\n')
		if err != nil {
			return off
		}
		fields := strings.Fields(strings.TrimPrefix(head, packMagic))
		if !strings.HasPrefix(head, packMagic) || len(fields) != 2 || len(fields[0]) != 64 {
			return off
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 {
			return off
		}
		payload := off + int64(len(head))
		if _, err := r.Discard(int(n)); err != nil {
			return off
		}
		if b, err := r.ReadByte(); err != nil || b != '\n' {
			return off
		}
		off = payload + n + 1
		each(fields[0], payload, n, off)
	}
}

// has is whether the Bundle named sum is kept, in a pack or as a file of its own.
func (p *packs) has(sum string) bool {
	p.mu.Lock()
	_, ok := p.index[sum]
	p.mu.Unlock()
	if ok {
		return true
	}
	if !p.legacy {
		return false
	}
	_, err := os.Stat(filepath.Join(p.dir, sum+".json"))
	return err == nil
}

// put keeps a Bundle and returns once it is on disk (shared with every other put waiting at the same time).
func (p *packs) put(raw []byte) (string, error) {
	sum, loc, err := p.add(raw)
	if err != nil || loc == nil {
		return sum, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return sum, p.waitSynced(*loc)
}

// add appends a Bundle without waiting for the sync; loc is nil when it was kept as an old single file.
func (p *packs) add(raw []byte) (string, *packLoc, error) {
	h := sha256.Sum256(raw)
	sum := hex.EncodeToString(h[:])
	if p.legacy {
		if _, err := os.Stat(filepath.Join(p.dir, sum+".json")); err == nil {
			return sum, nil, nil // kept before packs existed
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ready(); err != nil {
		return "", nil, err
	}
	loc, ok := p.index[sum]
	if !ok {
		if p.size >= packLimit {
			if err := p.next(); err != nil {
				return "", nil, err
			}
		}
		payload := seal.Blob("bundle", raw)
		head := fmt.Sprintf("%s%s %d\n", packMagic, sum, len(payload))
		entry := make([]byte, 0, len(head)+len(payload)+1)
		entry = append(append(append(entry, head...), payload...), '\n')
		if _, err := p.cur.Write(entry); err != nil {
			// A short write would leave the pack unreadable past it: cut it back to where it was.
			p.cur.Truncate(p.size)
			p.cur.Seek(p.size, io.SeekStart)
			return "", nil, err
		}
		loc = packLoc{seg: p.seg, off: p.size + int64(len(head)), length: int64(len(payload)), end: p.size + int64(len(entry))}
		p.size = loc.end
		p.scanned[p.seg] = p.size
		p.index[sum] = loc
	}
	return sum, &loc, nil
}

// ready opens the newest pack for writing, cutting off a torn last entry, or starts the first. p.mu is held.
func (p *packs) ready() error {
	if p.cur != nil {
		return nil
	}
	if p.writable {
		// Closed while idle (below) and known: open it again where it ended.
		f, err := os.OpenFile(packName(p.dir, p.seg), os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Seek(p.size, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		p.cur = f
		return nil
	}
	segs := segments(p.dir)
	if len(segs) == 0 {
		return p.start(1)
	}
	p.seg = segs[len(segs)-1]
	p.scanned[p.seg] = 0
	good := p.rescan()
	f, err := os.OpenFile(packName(p.dir, p.seg), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if info, err := f.Stat(); err == nil && info.Size() > good {
		// A power cut in the middle of the last entry: nothing names it, so it goes.
		if err := f.Truncate(good); err != nil {
			f.Close()
			return err
		}
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return err
	}
	p.cur, p.size, p.synced, p.writable = f, good, good, true
	return nil
}

// next closes the current pack, synced, and starts the following one. p.mu is held.
func (p *packs) next() error {
	for p.syncing {
		p.cond.Wait()
	}
	if err := p.cur.Sync(); err != nil {
		return err
	}
	p.cur.Close()
	p.cur = nil
	return p.start(p.seg + 1)
}

func (p *packs) start(seg int) error {
	f, err := os.OpenFile(packName(p.dir, seg), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// The new pack's name must survive a power cut too: once per pack, not per request.
	if err := fsx.SyncDir(p.dir); err != nil {
		f.Close()
		return err
	}
	p.cur, p.seg, p.size, p.synced, p.writable = f, seg, 0, 0, true
	p.scanned[seg] = 0
	return nil
}

// waitSynced returns once the pack holding loc is synced past it: one caller syncs, everyone who wrote before that
// sync began shares it. p.mu is held.
func (p *packs) waitSynced(loc packLoc) error {
	for loc.seg == p.seg && p.synced < loc.end {
		if p.syncing {
			p.cond.Wait()
			continue
		}
		p.syncing = true
		target, f := p.size, p.cur
		p.mu.Unlock()
		err := f.Sync()
		p.mu.Lock()
		p.syncing = false
		if err == nil && f == p.cur && target > p.synced {
			p.synced = target
		}
		p.cond.Broadcast()
		if err != nil {
			return err
		}
	}
	// Nothing left to sync and nobody syncing: the pack is closed until the next request, so no file is held open
	// while the server is idle (Windows cannot remove or replace a file that is open).
	if p.cur != nil && !p.syncing && p.synced == p.size {
		p.cur.Close()
		p.cur = nil
	}
	return nil
}

// get reads a kept Bundle back, checked against its name.
func (p *packs) get(sum string) ([]byte, error) {
	if p.single == "" && p.legacy {
		if raw, err := seal.ReadFile(filepath.Join(p.dir, sum+".json"), "bundle"); err == nil {
			return raw, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	p.mu.Lock()
	loc, ok := p.index[sum]
	if !ok {
		p.rescan()
		loc, ok = p.index[sum]
	}
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("bundle %s: %w", sum, os.ErrNotExist)
	}
	path := packName(p.dir, loc.seg)
	if p.single != "" {
		path = p.single
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, loc.length)
	if _, err := f.ReadAt(buf, loc.off); err != nil {
		return nil, fmt.Errorf("bundle %s in %s: %w", sum, filepath.Base(f.Name()), err)
	}
	raw, err := seal.Unblob("bundle", buf)
	if err != nil {
		return nil, fmt.Errorf("bundle %s in %s: %w", sum, filepath.Base(f.Name()), err)
	}
	if h := sha256.Sum256(raw); hex.EncodeToString(h[:]) != sum {
		return nil, fmt.Errorf("bundle %s in %s does not match its name (the pack was changed): %w", sum,
			filepath.Base(f.Name()), ErrBundleChanged)
	}
	return raw, nil
}

// close syncs and closes the pack being written.
func (p *packs) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.syncing {
		p.cond.Wait()
	}
	if p.cur == nil {
		return nil
	}
	err := p.cur.Sync()
	if cerr := p.cur.Close(); err == nil {
		err = cerr
	}
	p.cur = nil
	return err
}

// PackEntries counts the Bundles in a data directory's packs, sealed and plain (for vaglio data status).
func PackEntries(dir string) (sealed, plain int, err error) {
	for _, seg := range segments(dir) {
		f, err := os.Open(packName(dir, seg))
		if err != nil {
			return 0, 0, err
		}
		scanPack(f, 0, func(_ string, off, length, _ int64) {
			head := make([]byte, min(length, 64))
			f.ReadAt(head, off)
			if seal.Sealed(head) {
				sealed++
			} else {
				plain++
			}
		})
		f.Close()
	}
	return sealed, plain, nil
}

// SealPacks rewrites a data directory's packs with every plain entry sealed by the default key (vaglio data seal,
// with the server stopped). Each pack is written beside, read back and checked, then put in place. It returns how
// many entries it sealed.
func SealPacks(dir string) (int, error) {
	n := 0
	for _, seg := range segments(dir) {
		path := packName(dir, seg)
		f, err := os.Open(path)
		if err != nil {
			return n, err
		}
		var out bytes.Buffer
		var sums []string
		var rerr error
		count := 0
		scanPack(f, 0, func(sum string, off, length, _ int64) {
			buf := make([]byte, length)
			if _, err := f.ReadAt(buf, off); err != nil && rerr == nil {
				rerr = err
				return
			}
			if !seal.Sealed(buf) {
				count++
			}
			raw, err := seal.Unblob("bundle", buf)
			if err != nil && rerr == nil {
				rerr = err
				return
			}
			payload := seal.Blob("bundle", raw)
			fmt.Fprintf(&out, "%s%s %d\n", packMagic, sum, len(payload))
			out.Write(payload)
			out.WriteByte('\n')
			sums = append(sums, sum)
		})
		f.Close()
		if rerr != nil {
			return n, fmt.Errorf("%s: %w", path, rerr)
		}
		if count == 0 {
			continue
		}
		tmp := path + ".sealing"
		if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
			return n, err
		}
		check := openPacksAt(tmp)
		for _, sum := range sums {
			if _, err := check.get(sum); err != nil {
				os.Remove(tmp)
				return n, fmt.Errorf("%s: the sealed copy does not read back (%v); nothing changed", path, err)
			}
		}
		if err := fsx.Replace(tmp, path); err != nil {
			return n, err
		}
		n += count
	}
	if n > 0 {
		if err := fsx.SyncDir(dir); err != nil {
			return n, err
		}
	}
	return n, nil
}

// openPacksAt reads one pack file on its own (for checking a rewritten copy).
func openPacksAt(path string) *packs {
	p := &packs{dir: filepath.Dir(path), index: map[string]packLoc{}, scanned: map[int]int64{}}
	p.cond = sync.NewCond(&p.mu)
	if f, err := os.Open(path); err == nil {
		scanPack(f, 0, func(sum string, off, length, end int64) {
			p.index[sum] = packLoc{seg: -1, off: off, length: length, end: end}
		})
		f.Close()
	}
	p.single = path
	return p
}

// BundleNamed reads a kept Bundle by its SHA-256 name (a standby copying this server).
func (s *Store) BundleNamed(sum string) ([]byte, error) { return s.packs.get(sum) }

// Packs is a directory's pack files for a writer that syncs by itself: a standby copying a page of Bundles appends
// them all, then syncs once before it writes the records that name them.
type Packs struct{ p *packs }

// OpenPacks opens the pack files in dir (a data directory's bundles).
func OpenPacks(dir string) (*Packs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Packs{openPacks(dir)}, nil
}

// Has is whether the Bundle named sum is kept.
func (k *Packs) Has(sum string) bool { return k.p.has(sum) }

// Append keeps a Bundle, not yet synced; Sync makes it durable.
func (k *Packs) Append(raw []byte) (string, error) {
	sum, _, err := k.p.add(raw)
	return sum, err
}

// Sync returns once every Bundle appended before it is on disk.
func (k *Packs) Sync() error {
	k.p.mu.Lock()
	defer k.p.mu.Unlock()
	if k.p.cur == nil {
		return nil
	}
	return k.p.waitSynced(packLoc{seg: k.p.seg, end: k.p.size})
}

// Close syncs and closes the pack being written.
func (k *Packs) Close() error { return k.p.close() }
