package bunny

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

// FontRequest, FetchedFace, Result, PinnedFace are aliases for the source-agnostic types in internal/face so the dispatcher in cmds/ doesn't need per-source converters.
type FontRequest = face.Request
type FetchedFace = face.Face
type Result = face.Result
type PinnedFace = face.Pinned

const cssAPI = "https://fonts.bunny.net/css"

// httpClient is reused across requests so connection pooling kicks in for the chain of CSS+woff2 requests. 30 seconds matches the google driver's timeout.
var httpClient = &http.Client{Timeout: 30 * time.Second}

var fontURLPattern = regexp.MustCompile(`url\(\s*(https://fonts\.bunny\.net/[^)\s]+\.woff2)\s*\)`)
var fontFaceBlockPattern = regexp.MustCompile(`(?s)@font-face\s*\{([^}]+)\}`)
var fontWeightPattern = regexp.MustCompile(`font-weight:\s*(\d+)`)
var fontStylePattern = regexp.MustCompile(`font-style:\s*(\w+)`)
var unicodeRangePattern = regexp.MustCompile(`unicode-range:\s*([^;]+);`)

// Fetch resolves the (family, weights, italic, display) request against fonts.bunny.net, downloads every WOFF2 the response references, hashes each blob, and returns the populated Result. Disk cache works the same way as the google driver: keyed on sha256(URL).
func Fetch(req FontRequest, cacheDir string) (*Result, error) {
	if req.Family == "" {
		return nil, errors.New("font family is required")
	}
	cssURL := buildCSSURL(req)
	cssBytes, err := cachedGet(cssURL, "text/css", cacheDir)
	if err != nil {
		return nil, fmt.Errorf("fetch CSS for %q: %w", req.Family, err)
	}
	faces := parseFaces(string(cssBytes), req.Family)
	if len(faces) == 0 {
		return nil, fmt.Errorf("Bunny Fonts returned no WOFF2 URLs for %q — check the family name (Bunny uses lowercase-dashed form)", req.Family)
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

// FetchPinned replays URLs and content hashes from the lockfile, verifying each downloaded blob against the stored hash. Mismatch is a hard error — the user has to consciously refetch via `sova-fonts update`.
func FetchPinned(family string, pinned []PinnedFace, cacheDir string) (*Result, error) {
	if len(pinned) == 0 {
		return nil, fmt.Errorf("FetchPinned: no faces pinned for %q", family)
	}
	out := make([]FetchedFace, 0, len(pinned))
	for _, pf := range pinned {
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

// buildCSSURL serialises req into Bunny's CSS query syntax — the older Google-CSS1-style `family=name:400,400i,700` form. Italic faces are appended as `<weight>i` after the upright ones for visual grouping, then the full list is sorted to keep cache keys stable across reorderings.
func buildCSSURL(req FontRequest) string {
	weights := append([]int{}, req.Weights...)
	sort.Ints(weights)
	parts := make([]string, 0, len(weights)*2)
	for _, w := range weights {
		parts = append(parts, strconv.Itoa(w))
	}
	if req.Italic {
		for _, w := range weights {
			parts = append(parts, strconv.Itoa(w)+"i")
		}
	}
	family := bunnyFamilySlug(req.Family)
	display := req.Display
	if display == "" {
		display = "swap"
	}
	return fmt.Sprintf("%s?family=%s:%s&display=%s", cssAPI, family, strings.Join(parts, ","), display)
}

// bunnyFamilySlug converts the human display name into the dashed-lowercase form Bunny's URL expects. "JetBrains Mono" → "jetbrains-mono"; "Open Sans" → "open-sans"; consecutive separators collapse so "Source  Sans" still becomes "source-sans".
func bunnyFamilySlug(family string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(family) {
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

// parseFaces walks every @font-face block in the Bunny CSS response and extracts one FetchedFace per WOFF2 URL it finds. Multi-format `src:` declarations (woff2 + woff fallback) only surface the woff2 entry because that's what we ship; the woff fallback is for older browsers we're already not targeting.
func parseFaces(css, family string) []FetchedFace {
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
		}
		if sm := fontStylePattern.FindStringSubmatch(body); sm != nil {
			face.Italic = strings.EqualFold(strings.TrimSpace(sm[1]), "italic")
		}
		if rm := unicodeRangePattern.FindStringSubmatch(body); rm != nil {
			face.UnicodeRange = strings.TrimSpace(rm[1])
		}
		out = append(out, face)
	}
	return out
}

// cachedGet routes one HTTP GET through the on-disk cache. Cache key is sha256(url); same shape as the google driver.
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

// localFilename mirrors the google driver's pattern so staged filenames look the same regardless of source: `<family-slug>-<weight><italic-suffix>-<urlhash>.woff2`.
func localFilename(family string, face *FetchedFace) string {
	sum := sha256.Sum256([]byte(face.URL))
	hash := hex.EncodeToString(sum[:])[:12]
	style := ""
	if face.Italic {
		style = "i"
	}
	return fmt.Sprintf("%s-%d%s-%s.woff2", bunnyFamilySlug(family), face.Weight, style, hash)
}
