package orchestrator

import "encoding/json"

// jsonMarshalIndent renders a stage value as indented JSON with a trailing
// newline, giving every persisted stage file a stable, human-readable form.
func jsonMarshalIndent(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// jsonUnmarshal unmarshals raw JSON bytes into v.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
