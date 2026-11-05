# Text test fixtures

Tests load these faces explicitly, in a fixed fallback order, so results never depend on host font discovery or ordering.

| Fixture | Source | License |
| --- | --- | --- |
| Go Regular | `golang.org/x/image/font/gofont/goregular` v0.23.0, embedded as Go bytes; nothing is copied here | Go font license (BSD-style, Bigelow & Holmes), `font/gofont/ttfs/README` in that module |
| `NotoSansHebrew-Regular.ttf` | Debian `fonts-noto-core` 20201225-2build1, `/usr/share/fonts/truetype/noto/`, SHA-256 `436900d5ad77d33e4234247f3076eaecd25b92b9bea0519514f684140e3566a7` | SIL Open Font License 1.1, [NotoSansHebrew-OFL.txt](NotoSansHebrew-OFL.txt) |

Go Regular covers Latin, Greek and Cyrillic and has no Hebrew, so Hebrew text exercises per-rune fallback, right-to-left runs and mixed-direction lines. No color emoji face is vendored: Noto Color Emoji is about 11 MB. The emoji-space test loads `/usr/share/fonts/truetype/noto/NotoColorEmoji.ttf` by path when present and skips otherwise; its result is host evidence, recorded as such.
