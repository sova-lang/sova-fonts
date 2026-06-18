package google

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sova-lang/sova-fonts/internal/face"
)

// FontRequest, FetchedFace, Result, PinnedFace are aliases for the source-agnostic types in internal/face so callers can use either name interchangeably during the transition; new code should prefer the face.* names directly.
type FontRequest = face.Request
type FetchedFace = face.Face
type Result = face.Result
type PinnedFace = face.Pinned

// chromeUA is the User-Agent we send when calling the Google CSS endpoint. Modern browsers get WOFF2 URLs back; older UAs get WOFF or TTF, which would double the staged payload for no win. The specific Chrome version doesn't matter — Google just checks for a recent capability set.
const chromeUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// cssAPI is Google's CSS2 endpoint. CSS1 is deprecated; CSS2 supports axis pinning and the modern `wght@` / `ital,wght@` syntax we use.
const cssAPI = "https://fonts.googleapis.com/css2"

// httpClient is reused across all fetches so connection pooling kicks in for the chain of CSS+woff2 requests. 30 seconds is generous (woff2 files are 30-300 KB; on a flaky connection one request shouldn't tank the whole build).
var httpClient = &http.Client{Timeout: 30 * time.Second}

// fontURLPattern picks the WOFF2 URL out of a `src: url(https://fonts.gstatic.com/...woff2) format('woff2');` declaration. Lenient on whitespace; constrained to the gstatic origin to avoid false positives on user-injected CSS comments.
var fontURLPattern = regexp.MustCompile(`url\(\s*(https://fonts\.gstatic\.com/[^)\s]+\.woff2)\s*\)`)

// fontFaceBlockPattern is used to walk the Google CSS response one @font-face block at a time so the emitter sees the right weight/style/unicode-range per face. Multi-line, non-greedy.
var fontFaceBlockPattern = regexp.MustCompile(`(?s)@font-face\s*\{([^}]+)\}`)

// fontWeightPattern extracts the `font-weight` value from inside one face block. Captures both the discrete form (`font-weight: 400;` → group 1 = "400", group 2 = "") and the variable-font range form (`font-weight: 100 900;` → group 1 = "100", group 2 = "900").
var fontWeightPattern = regexp.MustCompile(`font-weight:\s*(\d+)(?:\s+(\d+))?`)

// fontStylePattern extracts the `font-style: italic;` value — `normal` or `italic` are the only values Google emits for our requests.
var fontStylePattern = regexp.MustCompile(`font-style:\s*(\w+)`)

// unicodeRangePattern extracts the `unicode-range: U+...;` value if present. Google often emits multiple @font-face blocks per weight, one per subset (latin, latin-ext, cyrillic, etc.) — preserving the range lets browsers pick the right blob per-glyph and avoid downloading unused subsets.
var unicodeRangePattern = regexp.MustCompile(`unicode-range:\s*([^;]+);`)

// Fetch resolves `req` against the live Google CSS endpoint and downloads every WOFF2 it references. All network reads route through the disk cache rooted at cacheDir (omit by passing ""); the cache key is the sha256 of the request URL so the same request always reads/writes the same blob and reusing the cache across builds is safe.
func Fetch(req FontRequest, cacheDir string) (*Result, error) {
	if req.Family == "" {
		return nil, errors.New("font family is required")
	}
	cssURL := buildCSSURL(req)
	cssBytes, err := cachedGet(cssURL, "text/css", cacheDir)
	if err != nil {
		return nil, fmt.Errorf("fetch CSS for %q: %w", req.Family, err)
	}
	faces, err := parseFaces(string(cssBytes), req.Family)
	if err != nil {
		return nil, fmt.Errorf("parse CSS for %q: %w", req.Family, err)
	}
	if len(faces) == 0 {
		return nil, fmt.Errorf("Google Fonts returned no WOFF2 URLs for %q — check the family name spelling and that the requested weights exist", req.Family)
	}
	for i := range faces {
		body, err := cachedGet(faces[i].URL, "font/woff2", cacheDir)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", faces[i].URL, err)
		}
		faces[i].Content = body
		faces[i].LocalName = localFilename(req.Family, &faces[i])
		sum := sha256.Sum256(body)
		faces[i].ContentHash = hex.EncodeToString(sum[:])
	}
	_ = cssBytes
	return &Result{Request: req, Faces: faces}, nil
}

// FetchPinned reuses the URLs and metadata recorded in a previous lockfile entry instead of hitting the Google CSS endpoint. Each pinned URL is fetched (or read from the disk cache) and its bytes hashed; the hash must match the locked one or the call fails — that detects upstream corruption / cache poisoning and forces the user to consciously refetch via `sova-fonts update`.
//
// LocalName and other metadata come from the lock so a deterministic build produces byte-identical staged filenames across machines.
func FetchPinned(family string, pinnedFaces []PinnedFace, cacheDir string) (*Result, error) {
	if len(pinnedFaces) == 0 {
		return nil, fmt.Errorf("FetchPinned: no faces pinned for %q", family)
	}
	out := make([]FetchedFace, 0, len(pinnedFaces))
	for _, pf := range pinnedFaces {
		body, err := cachedGet(pf.URL, "font/woff2", cacheDir)
		if err != nil {
			return nil, fmt.Errorf("fetch pinned %s: %w", pf.URL, err)
		}
		sum := sha256.Sum256(body)
		actual := hex.EncodeToString(sum[:])
		if actual != pf.ContentHash {
			return nil, fmt.Errorf("locked hash mismatch for %s: lock has %s, got %s — run `sova-fonts update` to refresh", pf.URL, pf.ContentHash, actual)
		}
		out = append(out, FetchedFace{
			Family:       family,
			Weight:       pf.Weight,
			WeightMax:    pf.WeightMax,
			Italic:       pf.Italic,
			UnicodeRange: pf.UnicodeRange,
			URL:          pf.URL,
			LocalName:    pf.LocalName,
			Content:      body,
			ContentHash:  actual,
		})
	}
	return &Result{Faces: out}, nil
}

// buildCSSURL serialises req into Google's CSS2 query syntax. Weights are always sorted ascending so a given request produces a stable URL (helps the disk cache stay warm across runs even if the user reordered their TOML).
//
// IMPORTANT: Google's CSS2 endpoint requires literal `+` for spaces in family names; `%2B` returns HTTP 400. Other reserved chars (`:`, `@`, `,`, `;`) are accepted both raw and percent-encoded, but we keep them raw to match the URLs shown in Google's own docs (and to keep cache keys readable when debugging).
func buildCSSURL(req FontRequest) string {
	family := strings.ReplaceAll(req.Family, " ", "+")
	display := req.Display
	if display == "" {
		display = "swap"
	}

	var familyAxis string
	if req.HasWeightRange() {
		rng := fmt.Sprintf("%d..%d", req.WeightRange[0], req.WeightRange[1])
		if req.Italic {
			familyAxis = fmt.Sprintf("%s:ital,wght@0,%s;1,%s", family, rng, rng)
		} else {
			familyAxis = fmt.Sprintf("%s:wght@%s", family, rng)
		}
	} else {
		weights := append([]int{}, req.Weights...)
		sort.Ints(weights)
		if req.Italic {
			pairs := make([]string, 0, len(weights)*2)
			for _, w := range weights {
				pairs = append(pairs, fmt.Sprintf("0,%d", w))
			}
			for _, w := range weights {
				pairs = append(pairs, fmt.Sprintf("1,%d", w))
			}
			familyAxis = fmt.Sprintf("%s:ital,wght@%s", family, strings.Join(pairs, ";"))
		} else {
			ws := make([]string, len(weights))
			for i, w := range weights {
				ws[i] = strconv.Itoa(w)
			}
			familyAxis = fmt.Sprintf("%s:wght@%s", family, strings.Join(ws, ";"))
		}
	}
	return fmt.Sprintf("%s?family=%s&display=%s", cssAPI, familyAxis, display)
}

// parseFaces walks every @font-face block in the CSS response and pulls out (family, weight, italic, unicode-range, url) tuples. One Google response typically contains many faces — one per weight × one per subset (latin, latin-ext, cyrillic, ...). We preserve all of them so the emitted CSS keeps the per-subset split: browsers download only the subsets they actually render.
func parseFaces(css, family string) ([]FetchedFace, error) {
	out := []FetchedFace{}
	blocks := fontFaceBlockPattern.FindAllStringSubmatch(css, -1)
	for _, b := range blocks {
		body := b[1]
		urlMatch := fontURLPattern.FindStringSubmatch(body)
		if urlMatch == nil {
			continue
		}
		face := FetchedFace{Family: family, URL: urlMatch[1]}
		if wm := fontWeightPattern.FindStringSubmatch(body); wm != nil {
			face.Weight, _ = strconv.Atoi(wm[1])
			if len(wm) > 2 && wm[2] != "" {
				face.WeightMax, _ = strconv.Atoi(wm[2])
			}
		}
		if sm := fontStylePattern.FindStringSubmatch(body); sm != nil {
			face.Italic = strings.EqualFold(strings.TrimSpace(sm[1]), "italic")
		}
		if rm := unicodeRangePattern.FindStringSubmatch(body); rm != nil {
			face.UnicodeRange = strings.TrimSpace(rm[1])
		}
		out = append(out, face)
	}
	return out, nil
}

// cachedGet routes one HTTP GET through the on-disk cache. Cache key is sha256(url) so collisions are impossible; the cached bytes are written atomically (tmp file + rename) so a half-finished download can't corrupt subsequent reads.
func cachedGet(u, acceptHint, cacheDir string) ([]byte, error) {
	if cacheDir != "" {
		sum := sha256.Sum256([]byte(u))
		key := hex.EncodeToString(sum[:])
		path := filepath.Join(cacheDir, key)
		if data, err := os.ReadFile(path); err == nil {
			return data, nil
		}
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", chromeUA)
	if acceptHint != "" {
		req.Header.Set("Accept", acceptHint)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, u, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, err
	}
	if cacheDir != "" {
		sum := sha256.Sum256([]byte(u))
		key := hex.EncodeToString(sum[:])
		_ = os.MkdirAll(cacheDir, 0o755)
		tmp := filepath.Join(cacheDir, key+".tmp")
		if err := os.WriteFile(tmp, body, 0o644); err == nil {
			_ = os.Rename(tmp, filepath.Join(cacheDir, key))
		}
	}
	return body, nil
}

// localFilename derives a stable, human-readable filename for one face. Naming pattern: `<family-slug>-<weight><italic-suffix>-<hash>.woff2` where hash is sha256[:12] of the URL (cache-busting via Google's font-version-pinned URL). Slug strips spaces; weight is the literal number; italic faces get an `i` suffix; subset distinction is encoded in the hash since the URLs differ per subset.
func localFilename(family string, face *FetchedFace) string {
	sum := sha256.Sum256([]byte(face.URL))
	hash := hex.EncodeToString(sum[:])[:12]
	slug := slugify(family)
	style := ""
	if face.Italic {
		style = "i"
	}
	return fmt.Sprintf("%s-%d%s-%s.woff2", slug, face.Weight, style, hash)
}

// slugify produces a filename-safe lowercase rendering of a family name: spaces → hyphens, non-alphanumeric stripped. "JetBrains Mono" → "jetbrains-mono".
func slugify(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ', r == '-', r == '_':
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// DefaultCacheDir returns the conventional location for the cross-build cache: `~/.sova/cache/fonts/`. Returning "" disables caching, which Fetch handles cleanly.
func DefaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".sova", "cache", "fonts")
}
