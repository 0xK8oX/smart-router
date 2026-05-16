package translation

import (
	"fmt"
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
	case "openai":
		// Passthrough — no modifications needed.
	default:
		return nil, fmt.Errorf("unsupported target format: %s", targetFormat)
	}

	return result, nil
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
