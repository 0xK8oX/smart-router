package translation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var dataURIMediaType = regexp.MustCompile(`^data:([^;]+);`)

// TranslateRequest converts a request body from sourceFormat to targetFormat.
func TranslateRequest(body map[string]interface{}, sourceFormat, targetFormat string) (map[string]interface{}, error) {
	if body == nil {
		return nil, fmt.Errorf("body is nil")
	}

	if sourceFormat == targetFormat {
		return body, nil
	}

	// Create a shallow copy so we don't mutate the caller's map.
	result := make(map[string]interface{}, len(body))
	for k, v := range body {
		result[k] = v
	}

	if sourceFormat == "openai" && targetFormat == "anthropic" {
		if _, ok := result["max_tokens"]; !ok {
			result["max_tokens"] = 4096
		}
		if err := convertOpenAIToAnthropic(result); err != nil {
			return nil, err
		}
		return result, nil
	}

	if sourceFormat == "anthropic" && targetFormat == "openai" {
		return TranslateAnthropicRequestToOpenAI(result)
	}

	return nil, fmt.Errorf("unsupported translation: %s -> %s", sourceFormat, targetFormat)
}

// convertOpenAIToAnthropic extracts system messages, converts tools, images,
// and tool-related messages to Anthropic-native formats.
func convertOpenAIToAnthropic(body map[string]interface{}) error {
	convertTools(body)
	convertToolChoice(body)
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
}

// convertToolChoice translates OpenAI tool_choice to Anthropic format.
func convertToolChoice(body map[string]interface{}) {
	raw, ok := body["tool_choice"]
	if !ok {
		return
	}

	switch tc := raw.(type) {
	case string:
		switch tc {
		case "auto":
			body["tool_choice"] = map[string]interface{}{"type": "auto"}
		case "required", "any":
			body["tool_choice"] = map[string]interface{}{"type": "any"}
		case "none":
			body["tool_choice"] = map[string]interface{}{"type": "none"}
		default:
			body["tool_choice"] = map[string]interface{}{"type": "auto"}
		}
	case map[string]interface{}:
		if tc["type"] == "function" {
			if name, ok := tc["function"].(map[string]interface{})["name"].(string); ok {
				body["tool_choice"] = map[string]interface{}{
					"type": "tool",
					"name": name,
				}
				return
			}
		}
		// Already Anthropic format or unknown — leave as-is.
	default:
		delete(body, "tool_choice")
	}
}

// convertMessages extracts system messages and converts tool-related messages,
// images, and content blocks to Anthropic-native formats.
func convertMessages(body map[string]interface{}) error {
	msgsRaw, ok := body["messages"]
	if !ok {
		return nil
	}
	msgs, ok := msgsRaw.([]interface{})
	if !ok {
		return nil
	}

	var systemParts []map[string]interface{}
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
			content := msg["content"]
			if text, ok := content.(string); ok && text != "" {
				systemParts = append(systemParts, map[string]interface{}{
					"type": "text",
					"text": text,
				})
			} else if arr, ok := content.([]interface{}); ok {
				for _, part := range arr {
					if p, ok := part.(map[string]interface{}); ok {
						systemParts = append(systemParts, p)
					}
				}
			}
			continue
		case "tool":
			toolCallID, _ := msg["tool_call_id"].(string)
			content := msg["content"]
			contentStr := ""
			if s, ok := content.(string); ok {
				contentStr = s
			} else {
				contentStr = stringify(content)
			}
			if toolCallID != "" {
				newMsgs = append(newMsgs, map[string]interface{}{
					"role": "user",
					"content": []map[string]interface{}{
						{
							"type":        "tool_result",
							"tool_use_id": toolCallID,
							"content":     contentStr,
						},
					},
				})
			} else {
				newMsgs = append(newMsgs, map[string]interface{}{
					"role":    "user",
					"content": "Tool result: " + contentStr,
				})
			}
			continue
		case "assistant":
			if tc, ok := msg["tool_calls"].([]interface{}); ok && len(tc) > 0 {
				var contentBlocks []map[string]interface{}

				// Preserve text content if present.
				textContent, _ := msg["content"].(string)
				if textContent != "" {
					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type": "text",
						"text": textContent,
					})
				}

				// Convert each tool_call to a tool_use block.
				for _, t := range tc {
					call, ok := t.(map[string]interface{})
					if !ok {
						continue
					}
					callID, _ := call["id"].(string)
					fn, _ := call["function"].(map[string]interface{})
					name, _ := fn["name"].(string)
					argsStr, _ := fn["arguments"].(string)

					var input map[string]interface{}
					if argsStr != "" {
						_ = json.Unmarshal([]byte(argsStr), &input)
					}
					if input == nil {
						input = map[string]interface{}{}
					}

					contentBlocks = append(contentBlocks, map[string]interface{}{
						"type":  "tool_use",
						"id":    callID,
						"name":  name,
						"input": input,
					})
				}

				newMsgs = append(newMsgs, map[string]interface{}{
					"role":    "assistant",
					"content": contentBlocks,
				})
				continue
			}
			// If assistant has array content (images, etc.), convert image_url blocks.
			if contentArr, ok := msg["content"].([]interface{}); ok {
				newMsgs = append(newMsgs, map[string]interface{}{
					"role":    "assistant",
					"content": convertOpenAIContentBlocks(contentArr),
				})
				continue
			}
		case "user":
			// Convert image_url content blocks to Anthropic image blocks.
			if contentArr, ok := msg["content"].([]interface{}); ok {
				newMsgs = append(newMsgs, map[string]interface{}{
					"role":    "user",
					"content": convertOpenAIContentBlocks(contentArr),
				})
				continue
			}
		}
		newMsgs = append(newMsgs, msg)
	}

	if len(systemParts) > 0 {
		if len(systemParts) == 1 {
			body["system"] = systemParts[0]["text"]
		} else {
			body["system"] = systemParts
		}
	}
	body["messages"] = newMsgs
	return nil
}

// convertOpenAIContentBlocks transforms OpenAI content blocks (image_url, text)
// into Anthropic content blocks.
func convertOpenAIContentBlocks(blocks []interface{}) []interface{} {
	var result []interface{}
	for _, b := range blocks {
		block, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "image_url":
			url := ""
			if u, ok := block["image_url"].(string); ok {
				url = u
			} else if obj, ok := block["image_url"].(map[string]interface{}); ok {
				url, _ = obj["url"].(string)
			}
			mediaType := "image/png"
			if m := dataURIMediaType.FindStringSubmatch(url); len(m) > 1 {
				mediaType = m[1]
			}
			data := strings.TrimPrefix(url, "data:"+mediaType+";base64,")
			result = append(result, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": mediaType,
					"data":       data,
				},
			})
		case "text":
			text, _ := block["text"].(string)
			result = append(result, map[string]interface{}{
				"type": "text",
				"text": text,
			})
		default:
			result = append(result, block)
		}
	}
	return result
}

// stringify returns a JSON string for any value, falling back to fmt.Sprint.
func stringify(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
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
			Type   string          `json:"type"`
			Text   string          `json:"text"`
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Input  json.RawMessage `json:"input"`
			Source struct {
				Type       string `json:"type"`
				MediaType  string `json:"media_type"`
				Data       string `json:"data"`
			} `json:"source"`
		} `json:"content"`
		Usage struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &anthropic); err != nil {
		return data, err
	}

	// Some providers (e.g., Kimi) return both Anthropic and OpenAI-style usage
	// fields but set input_tokens to 0. Fall back to prompt_tokens/completion_tokens.
	if anthropic.Usage.InputTokens == 0 && anthropic.Usage.PromptTokens > 0 {
		anthropic.Usage.InputTokens = anthropic.Usage.PromptTokens
	}
	if anthropic.Usage.OutputTokens == 0 && anthropic.Usage.CompletionTokens > 0 {
		anthropic.Usage.OutputTokens = anthropic.Usage.CompletionTokens
	}

	var content string
	var toolCalls []map[string]interface{}
	var imageURLs []map[string]interface{}
	for i, c := range anthropic.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "tool_use":
			args := "{}"
			if len(c.Input) > 0 {
				args = string(c.Input)
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":       c.ID,
				"type":     "function",
				"index":    i,
				"function": map[string]interface{}{
					"name":      c.Name,
					"arguments": args,
				},
			})
		case "image":
			imageURLs = append(imageURLs, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": fmt.Sprintf("data:%s;base64,%s", c.Source.MediaType, c.Source.Data),
				},
			})
		}
	}

	finishReason := anthropic.StopReason
	switch finishReason {
	case "end_turn":
		finishReason = "stop"
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	case "":
		finishReason = "stop"
	}

	msg := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		if content == "" {
			msg["content"] = nil
		}
	}
	if len(imageURLs) > 0 {
		if content != "" {
			imageURLs = append([]map[string]interface{}{{
				"type": "text",
				"text": content,
			}}, imageURLs...)
		}
		msg["content"] = imageURLs
	}

	openai := map[string]interface{}{
		"id":      "chatcmpl-" + anthropic.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   anthropic.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       msg,
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
