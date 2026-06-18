package cmds

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sova-lang/sova-fonts/internal/bunny"
	"github.com/sova-lang/sova-fonts/internal/config"
	"github.com/sova-lang/sova-fonts/internal/emit"
	"github.com/sova-lang/sova-fonts/internal/face"
	"github.com/sova-lang/sova-fonts/internal/google"
	"github.com/sova-lang/sova-fonts/internal/local"
	"github.com/sova-lang/sova-fonts/internal/lock"
	"github.com/spf13/cobra"
)

// newGenerateCmd registers `sova-fonts generate`. Reads sova-fonts.toml from --config (default ./sova-fonts.toml), reuses sova-fonts.lock when it matches the manifest, downloads (or reads from disk for source=local) the rest, stages WOFF2 files under output.fonts_dir, and writes the consolidated `.sova` file at output.sova_file. Idempotent — re-running with no manifest or lock change is a no-op modulo file mtimes.
func newGenerateCmd() *cobra.Command {
	var manifestPath string
	var noLock bool
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Download every font in sova-fonts.toml and write the generated Sova source",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runGenerate(manifestPath, generateOptions{ignoreLock: noLock})
			return err
		},
	}
	cmd.Flags().StringVar(&manifestPath, "config", "", "path to sova-fonts.toml (default: ./sova-fonts.toml)")
	cmd.Flags().BoolVar(&noLock, "no-lock", false, "ignore sova-fonts.lock and refetch every font from its source")
	return cmd
}

type generateOptions struct {
	// ignoreLock forces every font to be refetched from its upstream source even when a matching lock entry exists. Used by `sova-fonts update` to refresh pinned versions.
	ignoreLock bool
	// onlyFamilies, when non-empty, restricts the operation to the listed families. Used by `sova-fonts update <family>...` to refresh a subset; other entries keep their locked state.
	onlyFamilies map[string]bool
}

func runGenerate(manifestPath string, opts generateOptions) (*generateResult, error) {
	if manifestPath == "" {
		manifestPath = config.ManifestFilename
	}
	absManifest, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, err
	}
	projectRoot := filepath.Dir(absManifest)

	m, ok, err := config.Load(manifestPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s not found — run from a project root that contains it, or pass --config", manifestPath)
	}
	if err := config.Normalise(&m); err != nil {
		return nil, err
	}
	if len(m.Fonts) == 0 {
		fmt.Println("no [[font]] entries in", manifestPath, "— nothing to generate")
		return &generateResult{}, nil
	}

	lockPath := filepath.Join(projectRoot, lock.LockFilename)
	existingLock, _, err := lock.Load(lockPath)
	if err != nil {
		return nil, err
	}

	fontsDirAbs := filepath.Join(projectRoot, m.Output.FontsDir)
	if err := os.MkdirAll(fontsDirAbs, 0o755); err != nil {
		return nil, fmt.Errorf("create fonts dir %s: %w", fontsDirAbs, err)
	}
	sovaFileAbs := filepath.Join(projectRoot, m.Output.SovaFile)
	if err := os.MkdirAll(filepath.Dir(sovaFileAbs), 0o755); err != nil {
		return nil, fmt.Errorf("create gen dir %s: %w", filepath.Dir(sovaFileAbs), err)
	}

	cacheDir := google.DefaultCacheDir()
	results := make([]*face.Result, 0, len(m.Fonts))
	assetRelPaths := map[string]string{}
	newLock := lock.Lock{}
	var refetched, reused int

	for _, f := range m.Fonts {
		req := specToRequest(f)

		var res *face.Result
		var driverErr error

		shouldUseLock := !opts.ignoreLock
		if shouldUseLock && len(opts.onlyFamilies) > 0 && opts.onlyFamilies[f.Family] {
			shouldUseLock = false
		}
		if shouldUseLock {
			if pinned := existingLock.Find(f.Family, f.Source); pinned != nil && pinned.Matches(f.Weights, manifestWeightRange(f), f.Italic, f.Display) {
				pf := pinnedFromLock(pinned)
				res, driverErr = fetchPinnedBySource(f.Source, f.Family, pf, cacheDir)
				if driverErr == nil {
					res.Request = req
					reused++
					fmt.Printf("reusing locked %s — %d face(s)\n", f.Family, len(res.Faces))
				} else {
					fmt.Printf("locked %s failed (%s) — refetching from upstream\n", f.Family, driverErr)
					res = nil
				}
			}
		}
		if res == nil {
			fmt.Printf("fetching %s [%s] (weights=%v italic=%v)\n", f.Family, f.Source, f.Weights, f.Italic)
			res, err = fetchBySource(req, cacheDir, projectRoot)
			if err != nil {
				return nil, err
			}
			refetched++
		}

		for _, fc := range res.Faces {
			target := filepath.Join(fontsDirAbs, fc.LocalName)
			if err := os.WriteFile(target, fc.Content, 0o644); err != nil {
				return nil, fmt.Errorf("write %s: %w", target, err)
			}
			rel, err := emit.RelativeAssetPath(sovaFileAbs, target)
			if err != nil {
				return nil, err
			}
			assetRelPaths[fc.LocalName] = rel
		}
		results = append(results, res)
		newLock.Upsert(toLockedFont(f.Source, req, res))
	}

	source := emit.Emit(m, results, assetRelPaths)
	if err := os.WriteFile(sovaFileAbs, []byte(source), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", sovaFileAbs, err)
	}
	if err := lock.Save(lockPath, newLock); err != nil {
		return nil, fmt.Errorf("write %s: %w", lockPath, err)
	}

	fmt.Printf("wrote %s (%d font(s) — %d reused, %d refetched)\n", m.Output.SovaFile, len(results), reused, refetched)
	return &generateResult{Manifest: m, Reused: reused, Refetched: refetched, LockPath: lockPath}, nil
}

type generateResult struct {
	Manifest  config.Manifest
	Reused    int
	Refetched int
	LockPath  string
}

func specToRequest(f config.FontSpec) face.Request {
	req := face.Request{
		Family:      f.Family,
		Weights:     append([]int{}, f.Weights...),
		WeightRange: manifestWeightRange(f),
		Italic:      f.Italic,
		Display:     f.Display,
		Source:      f.Source,
	}
	for _, lf := range f.Files {
		req.Files = append(req.Files, face.LocalFile{Path: lf.Path, Weight: lf.Weight, WeightMax: lf.WeightMax, Italic: lf.Italic})
	}
	return req
}

// manifestWeightRange normalises the variable [2]int form of weight_range — empty / partial slices collapse to the zero value so HasWeightRange() / Matches() behave uniformly.
func manifestWeightRange(f config.FontSpec) [2]int {
	if len(f.WeightRange) != 2 {
		return [2]int{}
	}
	return [2]int{f.WeightRange[0], f.WeightRange[1]}
}

func fetchBySource(req face.Request, cacheDir, projectRoot string) (*face.Result, error) {
	switch req.Source {
	case "google":
		return google.Fetch(req, cacheDir)
	case "bunny":
		return bunny.Fetch(req, cacheDir)
	case "local":
		return local.Fetch(req, projectRoot)
	}
	return nil, fmt.Errorf("[[font]] %q: unknown source %q (supported: google, bunny, local)", req.Family, req.Source)
}

func fetchPinnedBySource(source, family string, pinned []face.Pinned, cacheDir string) (*face.Result, error) {
	switch source {
	case "google":
		return google.FetchPinned(family, pinned, cacheDir)
	case "bunny":
		return bunny.FetchPinned(family, pinned, cacheDir)
	case "local":
		return local.FetchPinned(family, pinned)
	}
	return nil, fmt.Errorf("unknown source %q", source)
}

func pinnedFromLock(lf *lock.LockedFont) []face.Pinned {
	out := make([]face.Pinned, 0, len(lf.Faces))
	for _, fc := range lf.Faces {
		out = append(out, face.Pinned{
			URL:          fc.URL,
			ContentHash:  fc.ContentHash,
			LocalName:    fc.LocalName,
			Weight:       fc.Weight,
			WeightMax:    fc.WeightMax,
			Italic:       fc.Italic,
			UnicodeRange: fc.UnicodeRange,
		})
	}
	return out
}

func toLockedFont(source string, req face.Request, res *face.Result) lock.LockedFont {
	lf := lock.LockedFont{
		Family:  req.Family,
		Source:  source,
		Italic:  req.Italic,
		Display: req.Display,
	}
	if req.HasWeightRange() {
		lf.WeightRange = []int{req.WeightRange[0], req.WeightRange[1]}
	} else {
		lf.Weights = append([]int{}, req.Weights...)
	}
	for _, fc := range res.Faces {
		lf.Faces = append(lf.Faces, lock.LockedFace{
			URL:          fc.URL,
			ContentHash:  fc.ContentHash,
			LocalName:    fc.LocalName,
			Weight:       fc.Weight,
			WeightMax:    fc.WeightMax,
			Italic:       fc.Italic,
			UnicodeRange: fc.UnicodeRange,
		})
	}
	return lf
}
