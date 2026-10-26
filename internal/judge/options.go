package judge

import "strings"

// ApplyThinking keeps the existing proxy flag and sends DeepSeek's native toggle.
func ApplyThinking(body map[string]any, model string, enabled bool) {
	if !enabled {
		body["enable_thinking"] = false
	}
	if strings.HasPrefix(strings.ToLower(model), "deepseek") {
		mode := "disabled"
		if enabled {
			mode = "enabled"
		}
		body["thinking"] = map[string]string{"type": mode}
	}
}
