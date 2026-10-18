package hugo_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/hugo"
)

func TestSlugifyTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "Hello World", "hello-world"},
		{"dashes collapse", "a--b   c", "a-b-c"},
		{"leading/trailing separators", "  --hi--  ", "hi"},
		{"accented lowercased", "Café Crème", "café-crème"},
		{"cjk kept", "海豚夺得冠军", "海豚夺得冠军"},
		{"cjk with latin", "重磅: Dolphins win", "重磅-dolphins-win"},
		{"url survives", "Re: [dolf/feed](https://x.example/a/b)", "re-dolf-feed-https-x-example-a-b"},
		{"empty", "", "slug-"},
		{"only punctuation", "!!??", "slug-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hugo.Slugify(tc.in)
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("Slugify(%q) = %q, want prefix %q", tc.in, got, tc.want)
			}
		})
	}
}

// The fallback (no letters or digits survived) ends in a 16-hex entropy
// suffix so otherwise-identical titles get distinct paths.
var fallbackSuffixRe = regexp.MustCompile(`^slug-[0-9a-f]{16}$`)

func TestSlugifyFallbackIsDeterministicPerInput(t *testing.T) {
	a := hugo.Slugify("!!??")
	b := hugo.Slugify("!!??")
	if !fallbackSuffixRe.MatchString(a) {
		t.Fatalf("fallback slug %q does not match %s", a, fallbackSuffixRe)
	}
	if a != b {
		t.Errorf("same input, different slugs: %q vs %q", a, b)
	}

	// Different hostile titles get different fallbacks (no collisions).
	seen := map[string]string{}
	for _, title := range []string{"!!??", "~~", "@@", "&&", "**", "(( ))"} {
		s := hugo.Slugify(title)
		if prev, dup := seen[s]; dup {
			t.Errorf("fallback collision between %q and %q: %q", prev, title, s)
		}
		seen[s] = title
	}
}

// Long titles truncate at the bound with a short entropy suffix, keeping the
// slug a safe path segment of predictable length.
func TestSlugifyTruncation(t *testing.T) {
	long := strings.Repeat("a-very-long-section-title-", 20) // 560 chars
	s := hugo.Slugify(long)
	if len([]rune(s)) > 93 { // 80 + "-" + 12 hex
		t.Errorf("slug of %d runes is %d runes, want <= 93", len([]rune(long)), len([]rune(s)))
	}
	if !regexp.MustCompile(`-[0-9a-f]{12}$`).MatchString(s) {
		t.Errorf("truncated slug %q lacks the 12-hex suffix", s)
	}
	// Truncation is deterministic for a given input.
	if again := hugo.Slugify(long); again != s {
		t.Errorf("truncation not deterministic: %q vs %q", s, again)
	}
}

// A long title with distinct words truncates on a word boundary (no trailing
// partial word): the slug ends with a complete final word plus the suffix.
func TestSlugifyTruncatesOnWordBoundary(t *testing.T) {
	title := strings.Repeat("alpha beta gamma delta ", 30)
	s := hugo.Slugify(title)
	tail := strings.TrimSuffix(s, regexp.MustCompile(`-[0-9a-f]{12}$`).FindString(s))
	if strings.HasSuffix(tail, "-") {
		t.Errorf("truncation left a dangling dash: %q", tail)
	}
	for _, word := range strings.Split(strings.TrimRight(tail, "-"), "-") {
		switch word {
		case "alpha", "beta", "gamma", "delta":
		default:
			t.Errorf("truncation split a word: %q (tail %q)", word, tail)
		}
	}
}
