package usage

import "encoding/json"

// readInstead lists tools whose alternative is opening the files they
// name. The difference between those file sizes and the response size
// is an upper bound, not a measurement: the agent might still open them.
var readInstead = map[string]bool{
	"get_symbol":      true,
	"file_outline":    true,
	"get_callers":     true,
	"get_callees":     true,
	"find_references": true,
	"explore":         true,
	"impact":          true,
	"branch_changes":  true,
	"project_map":     true,
}

// EstimateSaved returns how many response bytes were smaller than the
// files the result points at. Tools outside readInstead return 0.
func EstimateSaved(tool string, responseBytes int, fileBytes int64) int {
	if !readInstead[tool] || fileBytes <= 0 {
		return 0
	}
	saved := int(fileBytes) - responseBytes
	if saved < 0 {
		return 0
	}
	return saved
}

// ResultStats reads a tool's structured result: response size, whether
// the useful lists are empty, and the file paths it names.
func ResultStats(structured any) (responseBytes int, empty bool, files []string) {
	data, err := json.Marshal(structured)
	if err != nil || string(data) == "null" {
		return 0, false, nil
	}
	responseBytes = len(data)
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return responseBytes, false, nil
	}
	seen := map[string]bool{}
	collectFiles(v, seen)
	for f := range seen {
		files = append(files, f)
	}
	return responseBytes, listsEmpty(v), files
}

func collectFiles(v any, seen map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		if f, ok := t["file"].(string); ok && f != "" {
			seen[f] = true
		}
		for _, c := range t {
			collectFiles(c, seen)
		}
	case []any:
		for _, c := range t {
			collectFiles(c, seen)
		}
	}
}

func listsEmpty(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	saw := false
	for _, key := range []string{"results", "declarations", "files", "callers"} {
		arr, ok := m[key].([]any)
		if !ok {
			continue
		}
		saw = true
		if len(arr) > 0 {
			return false
		}
	}
	return saw
}
