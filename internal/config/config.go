package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// ManifestFilename is the conventional name of the project's font manifest. The generator looks for this in the current working directory; users running `sova-fonts generate` from anywhere else can pass an explicit path via `--config`.
const ManifestFilename = "sova-fonts.toml"

// Manifest is the parsed shape of sova-fonts.toml. Top-level `[output]` describes where staged files land; `[[font]]` blocks list each font family the project depends on. Order is preserved from the file so emitted `.sova` output reads top-to-bottom in declaration order — useful for stable diffs.
type Manifest struct {
	Output OutputSection `toml:"output"`
	Fonts  []FontSpec    `toml:"font"`
}

// OutputSection controls where `sova-fonts generate` writes its two products: the generated `.sova` file (a single file with all asset decls + CSS consts) and the directory the staged WOFF2 binaries are copied into. Both paths are resolved relative to the project root (the directory containing sova-fonts.toml). The Sova package name is declared explicitly so users keep control over where the gen file lives in their package tree.
type OutputSection struct {
	SovaFile    string `toml:"sova_file"`    // e.g. "src/gen/fonts.sova"
	FontsDir    string `toml:"fonts_dir"`    // e.g. "assets/fonts"
	SovaPackage string `toml:"sova_package"` // e.g. "myapp/gen"
	SovaSide    string `toml:"sova_side"`    // "shared" | "frontend" | "backend"; default "shared"
}

// FontSpec is one `[[font]]` entry. `Family` is the only required field for source = "google" or "bunny"; for source = "local", `Files` is required and `Weights`/`WeightRange`/`Italic` are ignored (each Files entry carries its own metadata). `WeightRange = [min, max]` (e.g. `[100, 900]`) asks the source for a variable font covering the range — only Google supports this; Bunny refuses with a clear diagnostic. WeightRange and Weights are mutually exclusive.
type FontSpec struct {
	Family      string      `toml:"family"`
	Weights     []int       `toml:"weights"`
	WeightRange []int       `toml:"weight_range,omitempty"`
	Italic      bool        `toml:"italic"`
	Display     string      `toml:"display"`
	Source      string      `toml:"source"`          // "google" (default), "bunny", "local"
	Files       []LocalFile `toml:"files,omitempty"` // only used when Source == "local"
}

// LocalFile is one entry inside a `[[font]] files = [...]` array — the local driver maps each file to a (weight, italic) tuple. Paths are resolved relative to the project root (the directory containing sova-fonts.toml). `WeightMax > 0` declares the file as a variable font covering [weight, weight_max] — emit renders the @font-face with `font-weight: <Weight> <WeightMax>;` so the browser instances the right weight.
type LocalFile struct {
	Path      string `toml:"path"`
	Weight    int    `toml:"weight"`
	WeightMax int    `toml:"weight_max,omitempty"`
	Italic    bool   `toml:"italic,omitempty"`
}

// Load reads and validates the manifest at path. Returns (zero, false, nil) when the file does not exist so the caller can distinguish "no manifest" from "manifest broken". Validation is intentionally lightweight here — full normalisation (default weights, default display) happens in Normalise so the on-disk file stays minimal and intent-preserving.
func Load(path string) (Manifest, bool, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, false, nil
		}
		return m, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &m); err != nil {
		return m, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, true, nil
}

// Normalise fills in default values that the user is allowed to omit from sova-fonts.toml. Mutates the manifest in place and returns it for chained-call convenience. Errors here are surfaced to the user as "your manifest is invalid" — every field with a default has one because forcing the user to type it would add noise without value.
func Normalise(m *Manifest) error {
	if m.Output.SovaFile == "" {
		m.Output.SovaFile = "src/gen/fonts.sova"
	}
	if m.Output.FontsDir == "" {
		m.Output.FontsDir = "assets/fonts"
	}
	if m.Output.SovaPackage == "" {
		return errors.New("[output] sova_package is required (e.g. \"myapp/gen\")")
	}
	if m.Output.SovaSide == "" {
		m.Output.SovaSide = "shared"
	}
	for i := range m.Fonts {
		f := &m.Fonts[i]
		if f.Family == "" {
			return fmt.Errorf("[[font]] #%d: `family` is required", i+1)
		}
		if f.Source == "" {
			f.Source = "google"
		}
		if len(f.WeightRange) > 0 {
			if len(f.WeightRange) != 2 {
				return fmt.Errorf("[[font]] %q: weight_range must be [min, max] (got %d values)", f.Family, len(f.WeightRange))
			}
			if f.WeightRange[0] < 1 || f.WeightRange[1] > 1000 || f.WeightRange[0] > f.WeightRange[1] {
				return fmt.Errorf("[[font]] %q: weight_range %v is invalid (need [min, max] with 1 <= min <= max <= 1000)", f.Family, f.WeightRange)
			}
			if len(f.Weights) > 0 {
				return fmt.Errorf("[[font]] %q: set either `weights` (discrete) or `weight_range` (variable), not both", f.Family)
			}
			if f.Source == "bunny" {
				return fmt.Errorf("[[font]] %q: weight_range is not supported by source=\"bunny\" (Bunny Fonts only serves discrete weights); use source=\"google\" or fall back to `weights = [...]`", f.Family)
			}
			if f.Source == "local" {
				return fmt.Errorf("[[font]] %q: weight_range belongs on individual entries in `files = [...]` (use `weight_max = ...` per file), not on the [[font]] block", f.Family)
			}
		}
		if f.Source != "local" && len(f.Weights) == 0 && len(f.WeightRange) == 0 {
			f.Weights = []int{400}
		}
		if f.Display == "" {
			f.Display = "swap"
		}
		if f.Source == "local" {
			if len(f.Files) == 0 {
				return fmt.Errorf("[[font]] %q: source = \"local\" needs a `files` array", f.Family)
			}
			for i, lf := range f.Files {
				if lf.WeightMax != 0 && (lf.WeightMax < lf.Weight || lf.WeightMax > 1000) {
					return fmt.Errorf("[[font]] %q file #%d (%s): weight_max %d invalid (must be >= weight=%d and <= 1000)", f.Family, i+1, lf.Path, lf.WeightMax, lf.Weight)
				}
			}
		}
	}
	return nil
}
