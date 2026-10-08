package store

import "encoding/json"

// jsonList serializes a list into a JSON array: an empty list turns into
// [], so that the column always holds an array and not null.
func jsonList(v []string) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// parseJSONList parses a JSON array from the keywords or allowlist column.
func parseJSONList(raw string) ([]string, error) {
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	return v, nil
}
