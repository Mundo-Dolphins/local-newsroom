package recipes

import (
	"fmt"
	"regexp"
	"strings"
)

// Instruction-override scan.
//
// Recipe text is untrusted content that ends up inside an LLM prompt. The
// engine's definitive safety boundary is CODE, not prompt text (deterministic
// claim validation, the independent final factual check, and the engine's
// non-disableable factual rules), so a malicious or compromised recipe file
// cannot loosen safety. The scan below is a second, conservative layer: it
// rejects recipe text that ATTEMPTS to override instructions at load time,
// which also catches typos that would produce a useless or incoherent run.
//
// The patterns are deliberately narrow (aim: zero false positives on
// legitimate editorial prose). They match classic instruction-override
// phrasings, not ordinary sports journalism.
var unsafePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bignore\s+(all\s+)?(previous|above|prior)\s+(instructions|prompts|context|rules)`),
	regexp.MustCompile(`(?i)\bdisregard\s+(all\s+|your\s+|the\s+)?(instructions|safety|rules|constraints|policies)`),
	regexp.MustCompile(`(?i)\byou\s+are\s+now\b`),
	regexp.MustCompile(`(?i)\bnew\s+(system\s+)?instructions?\b.*\b(follow|obey)\b`),
	regexp.MustCompile(`(?i)\boverride\s+the\s+(safety|security|factual|core|verification)`),
	regexp.MustCompile(`(?i)\bbypass\s+(the\s+|any\s+)?(verification|factual\s+check|fact[- ]?checking|safety|policy|rules|final\s+check)`),
	regexp.MustCompile(`(?i)\bdo\s+not\s+(verify|fact[- ]?check|validate)\b`),
	regexp.MustCompile(`(?i)\bskip\s+(the\s+)?(final\s+check|verification|fact[- ]?checking)`),
	regexp.MustCompile(`(?i)\b(reveal|output|print|repeat)\s+(your|the)\s+(system\s+)?prompt\b`),
	regexp.MustCompile(`(?i)\bact\s+as\s+if\s+(you\s+have\s+no|you\s+are\s+not\s+subject\s+to)\b`),
	regexp.MustCompile(`(?i)\bforget\s+(your|all|the)\s+(instructions|rules|safety)`),
	regexp.MustCompile(`(?i)\bdisclaim(er)?\s+that\s+facts\s+(are|can\s+be)\s+(fiction|invented|made\s+up)`),
}

// scanInstructionText rejects recipe instruction text that contains
// instruction-override phrasings. It is applied to every free-form text
// field at load time (fail-closed).
func scanInstructionText(field, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lowered := strings.ToLower(text)
	for _, p := range unsafePatterns {
		if m := p.FindString(lowered); m != "" {
			return fmt.Errorf("%w: %s contains instruction-override text %q — recipes may not attempt to override engine rules",
				ErrInvalid, field, strings.TrimSpace(m))
		}
	}
	return nil
}
