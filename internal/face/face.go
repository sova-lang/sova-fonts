package face

// Face is one @font-face descriptor: enough information to render a CSS rule, plus the binary blob to stage and the content-hash for the lockfile. URL is the original upstream URL (CDN or relative-to-project for local-file sources); LocalName is the on-disk filename relative to the project's fonts dir. WeightMax is non-zero only for variable fonts where the face covers a weight range — emit then renders `font-weight: <Weight> <WeightMax>;` instead of a single number.
type Face struct {
	Family       string
	Weight       int
	WeightMax    int
	Italic       bool
	UnicodeRange string
	URL          string
	LocalName    string
	Content      []byte
	ContentHash  string
}

// IsVariable reports whether this face covers a weight range rather than a single discrete weight. Variable-font WOFF2s are one file per subset that the browser instances at the right weight; the @font-face rule must declare the range so the browser knows which faces qualify for a given `font-weight: X` lookup.
func (f Face) IsVariable() bool { return f.WeightMax > 0 && f.WeightMax != f.Weight }

// Request is what the user wrote in `[[font]]` — what they want from upstream. Source dispatches to the right driver; Files is only used when Source == "local"; WeightRange (when non-zero) supersedes Weights and asks upstream for a variable font covering the [min, max] range — only Google supports this today, Bunny's CSS endpoint always serves discrete weights.
type Request struct {
	Family      string
	Weights     []int
	WeightRange [2]int
	Italic      bool
	Display     string
	Source      string
	Files       []LocalFile
}

// HasWeightRange reports whether a non-zero [min, max] was set. Convenience to avoid scattering `r.WeightRange[0] != 0 || r.WeightRange[1] != 0` checks across drivers.
func (r Request) HasWeightRange() bool { return r.WeightRange[0] != 0 || r.WeightRange[1] != 0 }

// LocalFile is one entry in a `[[font]] files = [...]` array — used by the local driver to map an on-disk file to a (weight, italic) pair. WeightMax > 0 declares the file as a variable font covering [Weight, WeightMax].
type LocalFile struct {
	Path      string
	Weight    int
	WeightMax int
	Italic    bool
}

// Result aggregates every face produced for one Request.
type Result struct {
	Request Request
	Faces   []Face
}

// Pinned is the lock-replay shape: the minimum fields a driver needs to reproduce a previous Result without going back to the source's CSS endpoint. URLs and hashes are pre-resolved; the driver just downloads (or re-reads from cache) and verifies. WeightMax mirrors Face.WeightMax for variable-font faces.
type Pinned struct {
	URL          string
	ContentHash  string
	LocalName    string
	Weight       int
	WeightMax    int
	Italic       bool
	UnicodeRange string
}
