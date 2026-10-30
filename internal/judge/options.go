package judge

import "strings"

// ApplyThinking applies each model's supported token and reasoning options.
func ApplyThinking(body map[string]any, model string, enabled bool) {
	if strings.EqualFold(model, "gpt-6-luna") {
		body["reasoning_effort"] = "none"
		if enabled {
			body["reasoning_effort"] = "medium"
			delete(body, "temperature")
		}
		if limit, ok := body["max_tokens"]; ok {
			body["max_completion_tokens"] = limit
			delete(body, "max_tokens")
		}
		return
	}
	if strings.HasPrefix(strings.ToLower(model), "claude") {
		// Claude's newer models reject custom temperature; thinking defaults to disabled.
		delete(body, "temperature")
		return
	}
	if !enabled {
		body["enable_thinking"] = false
	}
	if strings.HasPrefix(strings.ToLower(model), "deepseek") {
		mode := "disabled"
		if enabled {
			mode = "enabled"
		}
		body["thinking"] = map[string]string{"type": mode}
		if !enabled {
			// Some relays keep reasoning enabled despite the native switch.
			body["reasoning_effort"] = "low"
		}
	}
}

// ApplyJSONResponse is only for judgments; generated prose remains plain text.
func ApplyJSONResponse(body map[string]any, model string) {
	if strings.HasPrefix(strings.ToLower(model), "deepseek") || strings.EqualFold(model, "gpt-6-luna") {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
}
