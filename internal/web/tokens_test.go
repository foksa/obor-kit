package web

// The light theme's tone text reaches WCAG AA (docs/ui/docs-page-color.md, "Separate item"): a tone is text — a
// Pill's label on its own tint, accent links on the page — so each one keeps 4.5:1 against --surface, --bg,
// --surface-3 and the Pill's 12% tint of itself. The values come from web/src/app.css, not a copy: a token that slips
// back under the line fails here.

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// appCSS is the page's tokens, and lightBlocks its two light rules: the system's light mode and the toggle's choice.
var (
	appCSS      = filepath.Join("..", "..", "web", "src", "app.css")
	lightBlocks = []string{`:root:not([data-theme="dark"])`, `:root[data-theme="light"]`}
)

// toneTokens are the tones read as text in light mode (--done already passes and is not among them).
var toneTokens = []string{"--accent", "--ok", "--wait", "--bad", "--info"}

// TestLightToneText: every light tone reads at 4.5:1 on the surfaces it sits on, in both light blocks.
func TestLightToneText(t *testing.T) {
	css, err := os.ReadFile(appCSS)
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	for _, selector := range lightBlocks {
		tokens := cssTokens(t, string(css), selector)
		if tint := token(t, tokens, selector, "--tint"); tint != "12%" {
			t.Fatalf("%s: --tint is %q, not the 12%% pill tint this test measures", selector, tint)
		}
		surface := color(t, tokens, selector, "--surface")
		bg := color(t, tokens, selector, "--bg")
		surface3 := color(t, tokens, selector, "--surface-3")
		for _, name := range toneTokens {
			tone := color(t, tokens, selector, name)
			for _, p := range []struct {
				where  string
				behind rgb
			}{
				{"--surface", surface},
				{"--bg", bg},
				{"--surface-3", surface3},
				{"its 12% Pill tint", over(tone, 0.12, surface)},
			} {
				if got := contrast(tone, p.behind); got < 4.5 {
					t.Errorf("%s: %s on %s is %.2f:1, under 4.5:1", selector, name, p.where, got)
				}
			}
		}
	}
}

// cssToken is one custom property of a rule: its name and value.
var cssToken = regexp.MustCompile(`(--[\w-]+):\s*([^;]+);`)

// cssTokens are the custom properties of the rule whose selector is selector.
func cssTokens(t *testing.T, css, selector string) map[string]string {
	t.Helper()
	at := strings.Index(css, selector+" {")
	if at < 0 {
		t.Fatalf("app.css has no %q block", selector)
	}
	end := strings.Index(css[at:], "}")
	if end < 0 {
		t.Fatalf("app.css's %q block never closes", selector)
	}
	tokens := map[string]string{}
	for _, m := range cssToken.FindAllStringSubmatch(css[at:at+end], -1) {
		tokens[m[1]] = strings.TrimSpace(m[2])
	}
	return tokens
}

// token is tokens[name]; an absent one is an error, never an empty color.
func token(t *testing.T, tokens map[string]string, selector, name string) string {
	t.Helper()
	value, ok := tokens[name]
	if !ok {
		t.Fatalf("%s: app.css's block has no %s", selector, name)
	}
	return value
}

// rgb is a color's three 0-255 channels.
type rgb struct{ r, g, b int }

// color is tokens[name] as a color.
func color(t *testing.T, tokens map[string]string, selector, name string) rgb {
	t.Helper()
	value := token(t, tokens, selector, name)
	n, err := strconv.ParseUint(strings.TrimPrefix(value, "#"), 16, 32)
	if err != nil || len(value) != 7 || !strings.HasPrefix(value, "#") {
		t.Fatalf("%s: %s is not a six-digit hex color: %q", selector, name, value)
	}
	return rgb{int(n>>16) & 0xff, int(n>>8) & 0xff, int(n) & 0xff}
}

// luminance is WCAG's relative luminance.
func (c rgb) luminance() float64 {
	linear := func(v int) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.r) + 0.7152*linear(c.g) + 0.0722*linear(c.b)
}

// contrast is WCAG's contrast ratio between two colors.
func contrast(a, b rgb) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// over is c at alpha over base, as a Pill's color-mix(in srgb, c, transparent) renders on base.
func over(c rgb, alpha float64, base rgb) rgb {
	mix := func(v, behind int) int {
		return int(math.Round(alpha*float64(v) + (1-alpha)*float64(behind)))
	}
	return rgb{mix(c.r, base.r), mix(c.g, base.g), mix(c.b, base.b)}
}
