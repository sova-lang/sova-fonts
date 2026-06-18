package local

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sova-lang/sova-fonts/internal/face"
)

// FontRequest, FetchedFace, Result are aliases for the source-agnostic types in internal/face. Local source doesn't need a separate Pinned type because lock-replay for local files is trivial — just re-read the same paths and re-verify hashes via face.Pinned (the dispatcher handles the conversion).
type FontRequest = face.Request
type FetchedFace = face.Face
type Result = face.Result

// Fetch reads every file listed in req.Files relative to projectRoot, decoded from the user-supplied (path, weight, italic) tuples. The file extension determines the CSS `format(...)` hint at emit time; here we just propagate it through LocalName.
//
// Each file's content is sha256-hashed for the lockfile so future generates can detect when the user edited a file in place. LocalName uses the source filename verbatim (sanitised) plus a hash suffix so two unrelated WOFF2s named `Regular.woff2` in different directories don't collide.
func Fetch(req FontRequest, projectRoot string) (*Result, error) {
	if req.Family == "" {
		return nil, errors.New("font family is required")
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("[[font]] %q with source=\"local\" needs a non-empty `files` array (e.g. files = [{ path = \"./fonts/MyFont-400.woff2\", weight = 400 }])", req.Family)
	}
	out := make([]FetchedFace, 0, len(req.Files))
	for i, lf := range req.Files {
		if lf.Path == "" {
			return nil, fmt.Errorf("[[font]] %q file #%d: `path` is required", req.Family, i+1)
		}
		if lf.Weight == 0 {
			return nil, fmt.Errorf("[[font]] %q file #%d (%s): `weight` is required (e.g. 400)", req.Family, i+1, lf.Path)
		}
		abs := lf.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Clean(filepath.Join(projectRoot, lf.Path))
		}
		content, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", abs, err)
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		ext := strings.ToLower(filepath.Ext(abs))
		out = append(out, FetchedFace{
			Family:      req.Family,
			Weight:      lf.Weight,
			WeightMax:   lf.WeightMax,
			Italic:      lf.Italic,
			URL:         filepath.ToSlash(abs),
			LocalName:   localFilename(req.Family, lf.Weight, lf.Italic, hash, ext),
			Content:     content,
			ContentHash: hash,
		})
	}
	return &Result{Request: req, Faces: out}, nil
}

// FetchPinned for local sources just re-reads each path and verifies the hash. URLs in pinned entries are absolute (or project-relative) on-disk paths, not HTTP URLs, but the Pinned shape works fine — the URL field is opaque to the lock package.
func FetchPinned(family string, pinned []face.Pinned) (*Result, error) {
	if len(pinned) == 0 {
		return nil, fmt.Errorf("FetchPinned: no faces pinned for %q", family)
	}
	out := make([]FetchedFace, 0, len(pinned))
	for _, pf := range pinned {
		content, err := os.ReadFile(pf.URL)
		if err != nil {
			return nil, fmt.Errorf("read pinned %s: %w", pf.URL, err)
		}
		sum := sha256.Sum256(content)
		actual := hex.EncodeToString(sum[:])
		if actual != pf.ContentHash {
			return nil, fmt.Errorf("locked hash mismatch for %s: lock has %s, file is now %s — the source file changed; run `sova-fonts update` to accept the new bytes", pf.URL, pf.ContentHash, actual)
		}
		out = append(out, FetchedFace{
			Family:       family,
			Weight:       pf.Weight,
			WeightMax:    pf.WeightMax,
			Italic:       pf.Italic,
			UnicodeRange: pf.UnicodeRange,
			URL:          pf.URL,
			LocalName:    pf.LocalName,
			Content:      content,
			ContentHash:  actual,
		})
	}
	return &Result{Faces: out}, nil
}

// localFilename builds the staged filename. Pattern: `<family-slug>-<weight><italic-suffix>-<hash>.<ext>`. The hash prefix in the staged filename matches what `pass_resolve_assets` will hash on top of it, but the inner hash is the local-content sha256 so editing the source file in place produces a different staged filename (cache busting at our level even before the compiler adds its own hash).
func localFilename(family string, weight int, italic bool, hash, ext string) string {
	style := ""
	if italic {
		style = "i"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("%s-%d%s-%s%s", slugify(family), weight, style, hash[:12], ext)
}

func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == ' ', r == '-', r == '_':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}
