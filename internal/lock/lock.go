package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/BurntSushi/toml"
)

// LockFilename is the conventional name of the lockfile, written next to sova-fonts.toml. The on-disk schema is versioned via the top-level `version` field so we can break-change it later without confusing old readers.
const LockFilename = "sova-fonts.lock"

// Schema version 1 — bump when the field layout changes incompatibly. Readers that see a higher version than they support refuse to use the lock and fall back to refetching, which keeps old generators usable against newer lockfiles produced by a peer.
const SchemaVersion = 1

// Lock is the parsed shape of sova-fonts.lock. Top-level Version gates compatibility; Fonts holds one entry per resolved family, ordered the same way as the source manifest so diffs read top-to-bottom.
type Lock struct {
	Version int          `toml:"version"`
	Fonts   []LockedFont `toml:"font"`
}

// LockedFont pins one resolved font's full state: the request that produced it (so we can verify the manifest hasn't changed in incompatible ways) plus every face the fetcher returned with its content-hash. A user changing weights / weight_range / italic in the manifest invalidates this entry; the generator notices via Matches() and refetches.
type LockedFont struct {
	Family      string       `toml:"family"`
	Source      string       `toml:"source"`
	Weights     []int        `toml:"weights,omitempty"`
	WeightRange []int        `toml:"weight_range,omitempty"`
	Italic      bool         `toml:"italic"`
	Display     string       `toml:"display"`
	Faces       []LockedFace `toml:"face"`
}

// LockedFace is one @font-face descriptor with the cache-busting fields the generator needs to verify reuse. URL is the original CDN URL we fetched from; ContentHash is sha256(bytes); LocalName is the on-disk filename the emitter should stick into @asset(...). UnicodeRange survives so per-subset splitting reproduces faithfully. WeightMax (>0) marks the face as a variable-font slice covering [Weight, WeightMax].
type LockedFace struct {
	URL          string `toml:"url"`
	ContentHash  string `toml:"hash"`
	LocalName    string `toml:"local_name"`
	Weight       int    `toml:"weight"`
	WeightMax    int    `toml:"weight_max,omitempty"`
	Italic       bool   `toml:"italic"`
	UnicodeRange string `toml:"unicode_range,omitempty"`
}

// Load reads sova-fonts.lock from path. Returns (zero, false, nil) when the file does not exist; any other I/O or parse error is returned so the caller can decide between "fail loud" and "refetch quietly". Unknown schema versions log a warning and load as empty so the generator refetches instead of crashing on a future format.
func Load(path string) (Lock, bool, error) {
	var l Lock
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return l, false, nil
		}
		return l, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &l); err != nil {
		return l, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if l.Version > SchemaVersion {
		return Lock{}, false, nil
	}
	return l, true, nil
}

// Save writes the lock atomically (tmp-file + rename) so a crash mid-write can't corrupt a previously valid lock. Always stamps Version to the current SchemaVersion regardless of what's in l so callers don't have to remember.
func Save(path string, l Lock) error {
	l.Version = SchemaVersion
	for i := range l.Fonts {
		sort.Ints(l.Fonts[i].Weights)
		sort.SliceStable(l.Fonts[i].Faces, func(a, b int) bool {
			fa, fb := l.Fonts[i].Faces[a], l.Fonts[i].Faces[b]
			if fa.Weight != fb.Weight {
				return fa.Weight < fb.Weight
			}
			if fa.Italic != fb.Italic {
				return !fa.Italic
			}
			return fa.URL < fb.URL
		})
	}
	sort.SliceStable(l.Fonts, func(a, b int) bool {
		return l.Fonts[a].Family < l.Fonts[b].Family
	})
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	enc.Indent = "  "
	if err := enc.Encode(l); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Find returns the LockedFont entry matching the (family, source) tuple, or nil when no entry exists. Source disambiguates: switching the same family from "google" to "bunny" should refetch, not reuse.
func (l Lock) Find(family, source string) *LockedFont {
	for i := range l.Fonts {
		if l.Fonts[i].Family == family && l.Fonts[i].Source == source {
			return &l.Fonts[i]
		}
	}
	return nil
}

// Matches reports whether the locked entry's request fingerprint still matches the active manifest. A changed weight list, range, or flipped italic flag invalidates the entry; the lock then gets ignored for this font and the generator refetches. weightRange is [2]int{0,0} when unset.
func (lf LockedFont) Matches(weights []int, weightRange [2]int, italic bool, display string) bool {
	if lf.Italic != italic || lf.Display != display {
		return false
	}
	lockedRange := [2]int{0, 0}
	if len(lf.WeightRange) == 2 {
		lockedRange = [2]int{lf.WeightRange[0], lf.WeightRange[1]}
	}
	if lockedRange != weightRange {
		return false
	}
	if weightRange[0] != 0 || weightRange[1] != 0 {
		return true
	}
	if len(lf.Weights) != len(weights) {
		return false
	}
	a := append([]int{}, lf.Weights...)
	sort.Ints(a)
	b := append([]int{}, weights...)
	sort.Ints(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Upsert inserts or replaces the entry for (family, source). Mutates l in place; safe to call on a zero Lock.
func (l *Lock) Upsert(entry LockedFont) {
	for i := range l.Fonts {
		if l.Fonts[i].Family == entry.Family && l.Fonts[i].Source == entry.Source {
			l.Fonts[i] = entry
			return
		}
	}
	l.Fonts = append(l.Fonts, entry)
}

// Remove drops the entry matching (family, source). Returns true if an entry was removed.
func (l *Lock) Remove(family, source string) bool {
	for i := range l.Fonts {
		if l.Fonts[i].Family == family && l.Fonts[i].Source == source {
			l.Fonts = append(l.Fonts[:i], l.Fonts[i+1:]...)
			return true
		}
	}
	return false
}

// VerifyContent returns true when sha256(content) matches the recorded ContentHash. Caller uses this to decide whether a cached file on disk is still the file we locked against.
func (lf LockedFace) VerifyContent(content []byte) bool {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) == lf.ContentHash
}

// HashContent returns the hex sha256 we record in the lockfile. Centralised so callers don't accidentally use a different prefix length or encoding than the verifier expects.
func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
