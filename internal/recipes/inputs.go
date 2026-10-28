package recipes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ResolveInputs coerces and validates the values provided for a recipe's
// declared inputs, applying defaults.
//
// Rules:
//   - unknown input names are an error (typo protection; --input is a
//     closed vocabulary defined by the recipe);
//   - a provided value is coerced to the input's type and checked against
//     its constraints (choices, pattern, min/max, URL shape);
//   - a missing value falls back to the input's default (validated at load
//     time); if there is none and the input is required, resolution fails;
//   - optional inputs without a value or default are simply absent from the
//     result (recipients render templates with what exists).
//
// Resolved values are the ONLY way a recipe run receives user data: they are
// values, never code, never a bypass of engine constraints.
func (r *Recipe) ResolveInputs(values map[string]string) ([]ResolvedInput, error) {
	resolved := make([]ResolvedInput, 0, len(r.Inputs))
	provided := make(map[string]bool, len(values))
	for k := range values {
		provided[k] = true
	}
	for _, def := range r.Inputs {
		raw, hasValue := values[def.Name]
		if hasValue {
			raw = strings.TrimSpace(raw)
		}
		usedDefault := false
		if !hasValue || raw == "" {
			if def.Default != "" {
				raw = def.Default
				usedDefault = true
			} else if def.Required {
				return nil, fmt.Errorf("%w: missing required input %q (%s)", ErrInvalid, def.Name, describeConstraint(def))
			} else {
				continue // optional, absent
			}
		}
		v, err := coerceValue(def, raw)
		if err != nil {
			return nil, fmt.Errorf("%w: input %q: %s", ErrInvalid, def.Name, err)
		}
		resolved = append(resolved, ResolvedInput{
			Input:    def,
			Value:    v,
			Provided: !usedDefault,
		})
	}
	// Reject unknown input names (closed vocabulary).
	declared := make(map[string]bool, len(r.Inputs))
	for _, def := range r.Inputs {
		declared[def.Name] = true
	}
	for k := range values {
		if !declared[k] {
			return nil, fmt.Errorf("%w: unknown input %q (recipe %q declares: %s)",
				ErrInvalid, k, r.ID, declaredNames(r.Inputs))
		}
	}
	return resolved, nil
}

// describeConstraint renders a short human description of an input's
// accepted shape for error messages.
func describeConstraint(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s", in.Type)
	if in.Required {
		b.WriteString(" (required)")
	}
	if len(in.Choices) > 0 {
		fmt.Fprintf(&b, ", one of %s", strings.Join(in.Choices, "|"))
	}
	if in.Min != nil && in.Max != nil {
		fmt.Fprintf(&b, ", %d..%d", *in.Min, *in.Max)
	} else if in.Min != nil {
		fmt.Fprintf(&b, ", >= %d", *in.Min)
	} else if in.Max != nil {
		fmt.Fprintf(&b, ", <= %d", *in.Max)
	}
	return b.String()
}

func declaredNames(inputs []Input) string {
	if len(inputs) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(inputs))
	for _, in := range inputs {
		names = append(names, in.Name)
	}
	return strings.Join(names, ", ")
}

// coerceValue validates raw against the input definition and returns the
// canonical value (trimmed). Errors are plain (the caller wraps with the
// input name).
func coerceValue(in Input, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch in.Type {
	case InputTypeString:
		if in.Min != nil && len(v) < *in.Min {
			return "", fmt.Errorf("shorter than %d characters", *in.Min)
		}
		if in.Max != nil && len(v) > *in.Max {
			return "", fmt.Errorf("longer than %d characters", *in.Max)
		}
		if len(in.Choices) > 0 {
			ok := false
			for _, c := range in.Choices {
				if c == v {
					ok = true
					break
				}
			}
			if !ok {
				return "", fmt.Errorf("must be one of %s", strings.Join(in.Choices, ", "))
			}
		}
		if in.Pattern != "" {
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return "", fmt.Errorf("invalid pattern: %v", err)
			}
			if !re.MatchString(v) {
				return "", fmt.Errorf("does not match pattern %q", in.Pattern)
			}
		}
		return v, nil

	case InputTypeInt, InputTypeWeek:
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("must be an integer")
		}
		low, high := 0, 1<<30-1
		if in.Type == InputTypeWeek {
			low, high = 1, 17 // default NFL-style season range
		}
		if in.Min != nil {
			low = *in.Min
		}
		if in.Max != nil {
			high = *in.Max
		}
		if n < low || n > high {
			return "", fmt.Errorf("must be between %d and %d", low, high)
		}
		return strconv.Itoa(n), nil

	case InputTypeDate:
		if !dateValue(v) {
			return "", fmt.Errorf("must be a date (YYYY-MM-DD or RFC 3339 timestamp)")
		}
		return v, nil

	case InputTypeURL:
		if !urlValue(v) {
			return "", fmt.Errorf("must be an absolute http(s) URL")
		}
		return v, nil

	default:
		return "", fmt.Errorf("unknown input type %q", in.Type)
	}
}
