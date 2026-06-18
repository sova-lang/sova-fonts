# sova-fonts

External font-management generator for [Sova](https://sova-lang.dev) projects. Downloads fonts at build time from Google Fonts or Bunny Fonts (or reads them from local files), stages the WOFF2 binaries into your project's assets directory, and emits a single `.sova` file with `@asset(...)` declarations + ready-to-use `<Family>CSS` string consts.

Designed to pair with the Sova compiler's `[[build.codegen]]` hook — once configured, every `sova build` and `sova dev` re-runs the generator automatically when your font manifest changes.

## Install

```bash
go install github.com/sova-lang/sova-fonts@latest
```

Or download a release binary and put it on PATH.

## Quick start

### 1. Add `sova-fonts.toml` to your project

```toml
[output]
sova_file    = "src/gen/fonts.sova"      # where the generated Sova source lands
fonts_dir    = "assets/fonts"            # where staged font files are written
sova_package = "myapp/gen"               # package name for the gen file
sova_side    = "shared"                  # shared | frontend | backend (default shared)

[[font]]
family  = "Inter"
weights = [400, 700]

[[font]]
family  = "JetBrains Mono"
weights = [400]
italic  = true
display = "swap"                          # CSS font-display value (default "swap")
source  = "bunny"                         # google (default) | bunny | local

[[font]]
family = "MyCustom"
source = "local"
files  = [
    { path = "./fonts/MyCustom-Regular.woff2", weight = 400 },
    { path = "./fonts/MyCustom-Bold.woff2",    weight = 700 },
    { path = "./fonts/MyCustom-Italic.woff2",  weight = 400, italic = true },
]

[[font]]
family       = "Inter"
weight_range = [100, 900]                # one variable WOFF2 per subset, browser instances any weight in range
italic       = true
source       = "google"                   # variable-font ranges only supported on Google + local; Bunny refuses
```

### 2. Wire the generator into your Sova build

In `sova.toml`:

```toml
[[build.codegen]]
name    = "fonts"
command = "sova-fonts generate"
inputs  = ["sova-fonts.toml", "sova-fonts.lock"]
outputs = ["src/gen/fonts.sova", "assets/fonts/"]
```

The Sova compiler's codegen runner detects the manifest or lockfile changed and re-runs `sova-fonts generate` before the next compile.

### 3. Use the generated CSS in your components

```sova
import "myapp/gen"

type App with Composable, Component, Style {
    func style(): string {
        return gen.InterCSS + gen.JetBrainsMonoCSS + gen.MyCustomCSS +
            "body { font-family: 'Inter', sans-serif; }" +
            "code { font-family: 'JetBrains Mono', monospace; }"
    }

    func view(): Composable { ... }
}
```

The `<Family>CSS` const carries every `@font-face { ... src: url(...) format('woff2'); ... }` block ready for injection. URLs point at `/__sova/<staged-hash>.woff2` so the browser fetches them from your own server — no third-party connection, no flash-of-unstyled-text.

## Commands

| Command | What it does |
| --- | --- |
| `sova-fonts generate` | Resolve every font, stage WOFF2 files, write the gen `.sova`. Reuses entries from `sova-fonts.lock` when they match. |
| `sova-fonts generate --no-lock` | Ignore the lockfile and refetch every font (mostly useful for debugging — `update` is the better path for refreshing pins). |
| `sova-fonts update [family]...` | Refetch from upstream and rewrite the lock. With no args, refreshes all entries; with one or more families, refreshes only those. Use after a Google/Bunny version bump. |
| `sova-fonts add <family>` | Append a `[[font]]` entry to `sova-fonts.toml`. Flags: `-w 400,700`, `--weight-range "100..900"` (variable; mutually exclusive with `-w`), `--italic`, `--display swap`, `--source google\|bunny\|local`, `--generate` (also run `generate` after the add). |
| `sova-fonts remove <family>` | Drop the entry from both manifest and lock. Use `--prune-files` to also delete the staged WOFF2 files; `--source <name>` to remove only one source variant when the same family is pinned twice. |

All commands accept `--config <path>` if your manifest doesn't live in the current directory.

## Sources

### Google Fonts (`source = "google"`)

Resolves via [`fonts.googleapis.com/css2`](https://fonts.google.com/) — Google's modern axis-pinning endpoint. WOFF2-only response. Italic = both upright and italic faces for every weight.

### Bunny Fonts (`source = "bunny"`)

Resolves via [`fonts.bunny.net/css`](https://fonts.bunny.net/) — a privacy-friendly drop-in replacement for Google Fonts run by [bunny.net](https://bunny.net). Same font catalogue; same `[[font]]` shape. No User-Agent sniffing, no cookies, GDPR-friendly out of the box even for the initial CSS fetch (though once the WOFF2s are staged, neither source matters at runtime since users load them from your own server).

### Local files (`source = "local"`)

Reads WOFF2 / WOFF / TTF / OTF files from disk. Each entry in `files = [...]` maps one file to a `(weight, italic)` pair. Paths are resolved relative to `sova-fonts.toml`. Useful for fonts that aren't on Google/Bunny (custom commissions, foundry-licensed faces, variable fonts you've subset yourself).

```toml
[[font]]
family = "MyVariableFont"
source = "local"
files = [
    { path = "./vendor/MyFont-VF.woff2", weight = 400 },  # treat as the regular instance
]
```

## Lockfile

`sova-fonts.lock` pins every resolved (URL, sha256) pair so subsequent generates produce byte-identical output even when Google or Bunny rotates a font version under you. The lock is written automatically — you commit it alongside the manifest and never edit it by hand.

Flow:
- **`generate`** — for every font, if the lock has a matching entry the bytes are fetched (or read from `~/.sova/cache/fonts/`) and verified against the stored hash. Mismatch is a hard error. If the lock doesn't match the manifest (weights changed, italic flipped), the entry is refetched.
- **`update`** — bypasses the lock and refetches from upstream. The new bytes overwrite the lock.
- **`generate --no-lock`** — same as `update` but doesn't take a family list.

## Caching

Downloaded CSS responses and WOFF2 binaries are cached under `~/.sova/cache/fonts/` keyed by sha256(URL). Repeated runs across builds (or across machines sharing the cache dir) skip the network entirely.

## Why a separate tool

The Sova compiler intentionally stays free of network code and font-format knowledge. Build-time codegen runners like this one live as standalone binaries — same pattern as [ts2sova-generator](https://github.com/sova-lang/ts2sova-generator) and [browserx-generator](https://github.com/sova-lang/browserx-generator). The compiler discovers them via `[[build.codegen]]` in `sova.toml` and runs them before each build.

## Variable fonts

Set `weight_range = [min, max]` instead of `weights = [...]` to fetch one variable-font WOFF2 per subset that the browser instances at any weight in the range. The emitted `@font-face` carries `font-weight: <min> <max>;` so the browser knows which faces qualify for a given `font-weight: X` lookup.

```toml
[[font]]
family       = "Inter"
weight_range = [100, 900]
italic       = true                       # fetches both upright + italic variable faces
source       = "google"
```

For local variable-font files, declare the range per file:

```toml
[[font]]
family = "MyVariableFont"
source = "local"
files  = [
    { path = "./vendor/MyFont-VF.woff2",        weight = 100, weight_max = 900 },
    { path = "./vendor/MyFont-Italic-VF.woff2", weight = 100, weight_max = 900, italic = true },
]
```

Only Google and local sources support ranges today — Bunny Fonts' CSS endpoint always serves discrete weights, so `weight_range` with `source = "bunny"` is rejected at config-load time with a clear diagnostic.

## Roadmap

- Adobe Fonts driver (needs API token handling for the Typekit kit URL).
- `sova-fonts list` — print the manifest in a human-readable summary.
- Width / slant / optical-size axis pinning beyond `wght` (Google CSS2 exposes them).
- WOFF/TTF passthrough at the @font-face level for browsers without WOFF2 (almost none, but some embedded targets).
