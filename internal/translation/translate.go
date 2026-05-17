package translation

import (
	"fmt"
	"strings"
)

// TranslateRequest converts a request body to the target provider format.
// For now, OpenAI and Anthropic request formats are nearly identical.
// Just ensure required fields are present (e.g., max_tokens for anthropic).
func TranslateRequest(body map[string]interface{}, targetFormat string) (map[string]interface{}, error) {
	if body == nil {
		return nil, fmt.Errorf("body is nil")
	}

	// Create a shallow copy so we don't mutate the caller's map.
	result := make(map[string]interface{}, len(body))
	for k, v := range body {
		result[k] = v
	}

	switch targetFormat {
	case "anthropic":
		if _, ok := result["max_tokens"]; !ok {
			result["max_tokens"] = 4096
		}
		// Convert OpenAI messages (system in array) to Anthropic format (top-level system + user/assistant messages).
		if err := convertOpenAIToAnthropic(result); err != nil {
			return nil, err
		}
	case "openai":
		// Passthrough — no modifications needed.
	default:
		return nil, fmt.Errorf("unsupported target format: %s", targetFormat)
	}

	return result, nil
}

// convertOpenAIToAnthropic extracts system messages from the messages array into a
// top-level system field, keeping only user/assistant in the messages array.
func convertOpenAIToAnthropic(body map[string]interface{}) error {
	msgsRaw, ok := body["messages"]
	if !ok {
		return nil
	}
	msgs, ok := msgsRaw.([]interface{})
	if !ok {
		return nil
	}

	var systemParts []string
	var newMsgs []interface{}

	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			newMsgs = append(newMsgs, m)
			continue
		}
		role, _ := msg["role"].(string)
		if role == "system" {
			content, _ := msg["content"].(string)
			if content != "" {
				systemParts = append(systemParts, content)
			}
			continue
		}
		newMsgs = append(newMsgs, msg)
	}

	if len(systemParts) > 0 {
		body["system"] = strings.Join(systemParts, "\n")
	}
	body["messages"] = newMsgs
	return nil
}

// TranslateResponse converts a non-streaming response body.
// For now, pass through since formats are compatible enough.
func TranslateResponse(data []byte, fromFormat, toFormat string) ([]byte, error) {
	if fromFormat == toFormat {
		return data, nil
	}

	// Full conversion is complex; passthrough for now.
	return data, nil
}
