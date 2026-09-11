package plugin

import (
	"encoding/json"
	"strings"
)

// defaultStripTypes is the input[] entry type the zen free tier rejects.
var defaultStripTypes = []string{"additional_tools"}

// stripInputTypes removes input[] entries whose type is in stripTypes.
// Returns the rewritten body and whether anything changed. Non-array input,
// malformed JSON, and unknown entry types pass through untouched.
func stripInputTypes(body []byte, stripTypes []string) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	types := stripTypes
	if len(types) == 0 {
		types = defaultStripTypes
	}
	drop := make(map[string]struct{}, len(types))
	for _, t := range types {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			drop[t] = struct{}{}
		}
	}
	if len(drop) == 0 {
		return body, false
	}

	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, false
	}
	inputArr, ok := root["input"].([]any)
	if !ok {
		return body, false
	}

	keep := make([]any, 0, len(inputArr))
	changed := false
	for _, item := range inputArr {
		itemMap, ok := item.(map[string]any)
		if !ok {
			keep = append(keep, item)
			continue
		}
		itemType, _ := itemMap["type"].(string)
		if _, drop := drop[strings.ToLower(strings.TrimSpace(itemType))]; drop {
			changed = true
			continue
		}
		keep = append(keep, item)
	}

	if !changed {
		return body, false
	}
	root["input"] = keep
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}
