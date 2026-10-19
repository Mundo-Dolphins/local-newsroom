package bluesky

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
	"github.com/rivo/uniseg"
)

// Text limits of the app.bsky.feed.post lexicon: 300 graphemes and 3000
// bytes.
//
// The grapheme limit is the user-visible one; the byte limit guards the
// record encoding. Both are enforced with a fail-closed rejection — the
// adapter never truncates, because truncating approved content would break
// the digest binding (a publisher must send exactly what was approved).
const (
	maxGraphemes = 300
	maxBytes     = 3000
)

// langRe allows conservative language tags (the same shape the editorial
// presets accept: "en", "es", "pt-BR", "zh-Hans").
var langRe = regexp.MustCompile(`^[a-zA-Z]{2,8}(-[a-zA-Z0-9]{1,8})?$`)

// maxLangs is the app.bsky.feed.post lexicon bound on the langs array.
const maxLangs = 3

// maxFacets bounds the number of facets generated for one post (a newsroom
// post does not need hundreds of links; the bound keeps the record small).
const maxFacets = 32

// graphemeCount counts user-visible grapheme clusters (the unit the 300
// limit is expressed in): a multi-codepoint cluster (a flag, a family
// emoji, a combined letter) counts once.
func graphemeCount(text string) int {
	return uniseg.GraphemeClusterCount(text)
}

// validatePosts runs the full local content validation of a thread request
// and returns one issue per problem (empty when the thread is valid).
//
// This is purely local: no network. It runs for dry runs (issues are
// reported) and for real runs (any issue fails the call before the first
// write).
func validatePosts(posts []social.Post) []publication.DryRunIssue {
	var issues []publication.DryRunIssue
	addf := func(code, format string, args ...any) {
		issues = append(issues, publication.NewDryRunIssue(code, run.RedactSecrets(fmt.Sprintf(format, args...))))
	}

	if len(posts) == 0 {
		addf("thread_length", "the thread has no posts")
		return issues
	}
	if len(posts) > maxPosts {
		addf("thread_length", "the thread has %d posts, exceeding the adapter bound of %d", len(posts), maxPosts)
	}

	for i, p := range posts {
		which := fmt.Sprintf("post %d", i+1)

		text := p.Text
		if strings.TrimSpace(text) == "" {
			addf("empty_text", "%s is empty", which)
			continue
		}
		if bytes := len([]byte(text)); bytes > maxBytes {
			addf("over_byte_limit", "%s is %d bytes, exceeding the %d byte limit", which, bytes, maxBytes)
		}
		if g := graphemeCount(text); g > maxGraphemes {
			addf("over_grapheme_limit", "%s is %d graphemes, exceeding the %d grapheme limit", which, g, maxGraphemes)
		}
		if bad := controlByte(text); bad >= 0 {
			addf("invalid_text", "%s contains a control character (U+%04X) that posts cannot carry", which, bad)
		}

		if len(p.Langs) > maxLangs {
			addf("too_many_langs", "%s names %d languages, exceeding the bound of %d", which, len(p.Langs), maxLangs)
		}
		for _, l := range p.Langs {
			if !langRe.MatchString(l) {
				addf("invalid_lang", "%s names invalid language tag %q", which, l)
			}
		}

		if p.Reply != nil {
			addf("external_reply", "%s carries an external reply reference; v0.6 publishes self-contained threads only", which)
		}

		facetIssues := validateFacets(which, text, p.Facets)
		issues = append(issues, facetIssues...)
	}
	return issues
}

// maxPosts is the adapter's thread bound (tighter than nothing, wider than
// any newsroom thread; it also keeps the resumable receipt state inside the
// publication metadata bound).
const maxPosts = social.MaxPosts

// controlByte reports the first C0 control character other than tab,
// newline, or carriage return (NUL and friends cannot appear in a post).
func controlByte(text string) int {
	for _, r := range text {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return int(r)
		}
	}
	return -1
}

// validateFacets checks explicit facets: byte-offset bounds, ordering,
// overlap, and the feature types v0.6 supports.
//
// Byte offsets are validated against the post text's UTF-8 bytes — the
// same offsets the record will carry — so a facet is either exactly over
// the text it annotates or it is rejected before any write.
func validateFacets(which, text string, facets []social.Facet) []publication.DryRunIssue {
	var issues []publication.DryRunIssue
	addf := func(code, format string, args ...any) {
		issues = append(issues, publication.NewDryRunIssue(code, run.RedactSecrets(fmt.Sprintf(format, args...))))
	}

	total := len([]byte(text))
	prevEnd := -1
	for i, f := range facets {
		span := fmt.Sprintf("%s facet %d", which, i+1)
		if f.ByteStart < 0 || f.ByteEnd <= f.ByteStart || f.ByteEnd > total {
			addf("invalid_facet", "%s spans byte %d..%d, outside the text (0..%d)", span, f.ByteStart, f.ByteEnd, total)
			continue
		}
		if f.ByteStart < prevEnd {
			addf("overlapping_facets", "%s (byte %d..%d) overlaps the previous facet (ending at byte %d)", span, f.ByteStart, f.ByteEnd, prevEnd)
		}
		prevEnd = f.ByteEnd
		if len(f.Features) == 0 {
			addf("empty_facet", "%s carries no features", span)
			continue
		}
		for _, feat := range f.Features {
			switch feat.Type {
			case social.FeatureLink:
				uri := feat.Fields["uri"]
				if !isSafeLinkURI(uri) {
					addf("invalid_facet", "%s link feature has no valid http(s) uri", span)
				}
			case social.FeatureMention, social.FeatureTag:
				// Mention and tag facets require identity resolution or
				// hashtag semantics the v0.6 adapter does not perform;
				// fail closed instead of sending half-formed features.
				addf("deferred_facet", "%s carries a %s feature, which the v0.6 adapter does not support (mentions are deferred)", span, feat.Type)
			default:
				addf("invalid_facet", "%s carries unknown feature type %q", span, feat.Type)
			}
		}
	}
	return issues
}

// isSafeLinkURI reports whether uri is an absolute http(s) URL without
// userinfo (a link facet must point where it says).
func isSafeLinkURI(uri string) bool {
	if uri == "" {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.User != nil {
		return false
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return true
}
