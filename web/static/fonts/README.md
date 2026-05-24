# Bundled web fonts

Fonts are baked into the binary via `go:embed web/dist/**` (the static
adapter ships `web/static/` into `web/dist/`). No runtime download —
the air-gapped department deploy stays sealed.

## Files

| File | License | Bytes (approx) | Used for |
|---|---|---|---|
| `Cairo-VariableFont.woff2` | SIL OFL 1.1 | ~140 KB | **Primary face** — Latin + Arabic |
| `InterVariable.woff2` | SIL OFL 1.1 | ~360 KB | Fallback, Latin only |
| `InterVariable-Italic.woff2` | SIL OFL 1.1 | ~360 KB | Fallback, Latin italic |
| `NotoKufiArabic-arabic.woff2` | SIL OFL 1.1 | ~80 KB | Fallback, Arabic glyphs only |

## Why Cairo

Cairo carries Latin and Arabic glyphs in a single variable-axis file,
which keeps the embedded payload small while supporting RTL without a
second font request. The fallback chain (`Inter Variable` →
`Noto Kufi Arabic` → `system-ui`) only paints if Cairo fails to load
at first paint.

## Refreshing Cairo

The Cairo variable woff2 is sourced from the Google Fonts repository
on GitHub. Run `fetch-cairo.sh` from a workstation with internet
access (NOT from the air-gapped build host) to refresh the file:

```sh
cd web/static/fonts/
./fetch-cairo.sh
```

The script downloads `Cairo[slnt,wght].ttf` from the
`google/fonts` GitHub repo, validates the SHA-256, and converts it
to a subsetted woff2 using `fonttools`. The result lands at
`Cairo-VariableFont.woff2` and is committed to git.

After updating the file, run `cd web && npm run build` to refresh
the embedded bundle.
