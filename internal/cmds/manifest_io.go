package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/sova-lang/sova-fonts/internal/config"
)

// loadOrInit returns the manifest at path, creating an empty in-memory shell when the file does not exist yet. The returned path is the absolute file path the caller should write back to (always rooted at manifestPath; we don't auto-relocate). The returned bool reports whether the file existed on disk — useful for callers that want to surface "created new manifest" feedback.
func loadOrInit(manifestPath string) (config.Manifest, string, bool, error) {
	if manifestPath == "" {
		manifestPath = config.ManifestFilename
	}
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return config.Manifest{}, "", false, err
	}
	m, exists, err := config.Load(manifestPath)
	if err != nil {
		return config.Manifest{}, abs, false, err
	}
	return m, abs, exists, nil
}

// saveManifest re-marshals the manifest and writes it atomically. Comments and whitespace from the original file are NOT preserved — that's a deliberate tradeoff for using a plain-Go round-trip rather than a structural TOML editor. Users who want preserved formatting should hand-edit the manifest themselves; the `add`/`remove` commands are a convenience for the common case.
func saveManifest(path string, m config.Manifest) error {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = "  "
	if err := enc.Encode(m); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// upsertFont inserts the spec into m or replaces an existing entry with the same family + source. Returns whether the operation was an insert (true) or a replace (false). Family names are compared case-insensitively because Google Fonts treats "inter" and "Inter" the same way.
func upsertFont(m *config.Manifest, spec config.FontSpec) (inserted bool) {
	for i := range m.Fonts {
		if strings.EqualFold(m.Fonts[i].Family, spec.Family) && m.Fonts[i].Source == spec.Source {
			m.Fonts[i] = spec
			return false
		}
	}
	m.Fonts = append(m.Fonts, spec)
	return true
}

// removeFont drops the first entry matching family (case-insensitive). Returns true if an entry was removed. When source is non-empty, only entries with that source match — lets users remove just the Google variant of a family they also pinned locally.
func removeFont(m *config.Manifest, family, source string) bool {
	for i := range m.Fonts {
		if !strings.EqualFold(m.Fonts[i].Family, family) {
			continue
		}
		if source != "" && m.Fonts[i].Source != source {
			continue
		}
		m.Fonts = append(m.Fonts[:i], m.Fonts[i+1:]...)
		return true
	}
	return false
}

// parseWeightRange converts a "min..max" string (e.g. "100..900") into the two endpoints. Rejects malformed input and ranges where min > max or anything is outside 1..1000. Used by `sova-fonts add --weight-range "100..900"` to populate the weight_range field for variable fonts.
func parseWeightRange(raw string) (int, int, error) {
	parts := strings.Split(raw, "..")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid weight range %q (expected MIN..MAX, e.g. \"100..900\")", raw)
	}
	var lo, hi int
	if _, err := fmt.Sscanf(strings.TrimSpace(parts[0]), "%d", &lo); err != nil {
		return 0, 0, fmt.Errorf("invalid weight range %q: %w", raw, err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%d", &hi); err != nil {
		return 0, 0, fmt.Errorf("invalid weight range %q: %w", raw, err)
	}
	if lo < 1 || hi > 1000 || lo > hi {
		return 0, 0, fmt.Errorf("weight range %d..%d invalid (need 1 <= min <= max <= 1000)", lo, hi)
	}
	return lo, hi, nil
}

// parseWeightList converts the CSV weights flag ("400,700") into a sorted int slice. Reject zeros / negatives / >1000 with a clear error so the user doesn't silently end up with `weights = [0]`.
func parseWeightList(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil {
			return nil, fmt.Errorf("invalid weight %q (must be an integer)", p)
		}
		if n < 1 || n > 1000 {
			return nil, fmt.Errorf("weight %d out of range (1..1000)", n)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("no weights provided")
	}
	return out, nil
}
