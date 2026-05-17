package translation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
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
// top-level system field, converts tools from OpenAI to Anthropic format, and
// strips tool_choice. Tool-related messages are converted to plain text.
func convertOpenAIToAnthropic(body map[string]interface{}) error {
	convertTools(body)
	if err := convertMessages(body); err != nil {
		return err
	}
	return nil
}

// convertTools transforms OpenAI-format tools to Anthropic format.
func convertTools(body map[string]interface{}) {
	toolsRaw, ok := body["tools"]
	if !ok {
		return
	}
	tools, ok := toolsRaw.([]interface{})
	if !ok {
		delete(body, "tools")
		delete(body, "tool_choice")
		return
	}

	var newTools []interface{}
	for _, t := range tools {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]interface{})
		if !ok {
			continue
		}
		newTool := map[string]interface{}{
			"name": fn["name"],
		}
		if desc, ok := fn["description"].(string); ok {
			newTool["description"] = desc
		}
		if params, ok := fn["parameters"].(map[string]interface{}); ok {
			newTool["input_schema"] = params
		}
		newTools = append(newTools, newTool)
	}

	if len(newTools) > 0 {
		body["tools"] = newTools
	} else {
		delete(body, "tools")
	}
	delete(body, "tool_choice")
}

// convertMessages extracts system messages and converts tool messages to plain text.
func convertMessages(body map[string]interface{}) error {
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
		switch role {
		case "system":
			content, _ := msg["content"].(string)
			if content != "" {
				systemParts = append(systemParts, content)
			}
			continue
		case "tool":
			// Convert tool result to a plain user message.
			content, _ := msg["content"].(string)
			newMsgs = append(newMsgs, map[string]interface{}{
				"role":    "user",
				"content": "Tool result: " + content,
			})
			continue
		case "assistant":
			// If assistant has tool_calls, convert to plain text.
			if tc, ok := msg["tool_calls"].([]interface{}); ok && len(tc) > 0 {
				var parts []string
				for _, t := range tc {
					call, ok := t.(map[string]interface{})
					if !ok {
						continue
					}
					fn, _ := call["function"].(map[string]interface{})
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					parts = append(parts, fmt.Sprintf("Using tool %s(%s)", name, args))
				}
				content, _ := msg["content"].(string)
				if content != "" {
					parts = append([]string{content}, parts...)
				}
				newMsgs = append(newMsgs, map[string]interface{}{
					"role":    "assistant",
					"content": strings.Join(parts, "\n"),
				})
				continue
			}
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
func TranslateResponse(data []byte, fromFormat, toFormat string) ([]byte, error) {
	if fromFormat == toFormat {
		return data, nil
	}
	if fromFormat == "anthropic" && toFormat == "openai" {
		return translateAnthropicToOpenAI(data)
	}
	return data, nil
}

func translateAnthropicToOpenAI(data []byte) ([]byte, error) {
	var anthropic struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &anthropic); err != nil {
		return data, err
	}

	var content string
	for _, c := range anthropic.Content {
		if c.Type == "text" {
			content += c.Text
		}
	}

	finishReason := anthropic.StopReason
	switch finishReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "":
		finishReason = "stop"
	}

	openai := map[string]interface{}{
		"id":      "chatcmpl-" + anthropic.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   anthropic.Model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     anthropic.Usage.InputTokens,
			"completion_tokens": anthropic.Usage.OutputTokens,
			"total_tokens":      anthropic.Usage.InputTokens + anthropic.Usage.OutputTokens,
		},
	}

	return json.Marshal(openai)
}
