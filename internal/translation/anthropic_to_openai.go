package translation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicSystemText extracts plain text from an Anthropic system message
// content field (a string, or an array of {type:"text", text:"..."} blocks).
func anthropicSystemText(content interface{}) string {
	switch s := content.(type) {
	case string:
		return s
	case []interface{}:
		var parts []string
		for _, p := range s {
			if pm, ok := p.(map[string]interface{}); ok {
				if text, ok := pm["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return stringify(content)
}
func TranslateAnthropicRequestToOpenAI(body map[string]interface{}) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Pass through model
	if model, ok := body["model"].(string); ok {
		result["model"] = model
	}

	// Pass through standard params
	for _, k := range []string{"max_tokens", "temperature", "top_p", "stream", "metadata"} {
		if v, ok := body[k]; ok {
			result[k] = v
		}
	}

	// Build messages array
	var messages []interface{}

	// Prepend system as first message
	if sys, ok := body["system"]; ok {
		var systemText string
		switch s := sys.(type) {
		case string:
			systemText = s
		case []interface{}:
			var parts []string
			for _, p := range s {
				if pm, ok := p.(map[string]interface{}); ok {
					if text, ok := pm["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "system",
				"content": systemText,
			})
		}
	}

	// Convert messages
	if msgsRaw, ok := body["messages"].([]interface{}); ok {
		for _, m := range msgsRaw {
			msg, ok := m.(map[string]interface{})
			if !ok {
				messages = append(messages, m)
				continue
			}
			role, _ := msg["role"].(string)
			content := msg["content"]

			switch role {
			case "assistant":
				if contentArr, ok := content.([]interface{}); ok {
					var textParts []string
					var toolCalls []map[string]interface{}
					for _, c := range contentArr {
						block, ok := c.(map[string]interface{})
						if !ok {
							continue
						}
						blockType, _ := block["type"].(string)
						switch blockType {
						case "text":
							if text, ok := block["text"].(string); ok {
								textParts = append(textParts, text)
							}
						case "thinking":
							if t, ok := block["thinking"].(string); ok {
								textParts = append(textParts, t)
							}
						case "tool_use":
							id, _ := block["id"].(string)
							name, _ := block["name"].(string)
							input := block["input"]
							args := "{}"
							if input != nil {
								if b, err := json.Marshal(input); err == nil {
									args = string(b)
								}
							}
							toolCalls = append(toolCalls, map[string]interface{}{
								"id":   id,
								"type": "function",
								"function": map[string]interface{}{
									"name":      name,
									"arguments": args,
								},
							})
						}
					}
					assistantMsg := map[string]interface{}{
						"role": "assistant",
					}
					if len(textParts) > 0 {
						assistantMsg["content"] = strings.Join(textParts, "")
					}
					if len(toolCalls) > 0 {
						assistantMsg["tool_calls"] = toolCalls
						if assistantMsg["content"] == nil {
							assistantMsg["content"] = nil
						}
					}
					messages = append(messages, assistantMsg)
				} else {
					messages = append(messages, msg)
				}

			case "user":
				if contentArr, ok := content.([]interface{}); ok {
					var toolResults []map[string]interface{}
					var otherParts []interface{}

					for _, c := range contentArr {
						block, ok := c.(map[string]interface{})
						if !ok {
							otherParts = append(otherParts, c)
							continue
						}
						blockType, _ := block["type"].(string)
						if blockType == "tool_result" {
							toolUseID, _ := block["tool_use_id"].(string)
							trContent := block["content"]
							contentStr := ""
							if s, ok := trContent.(string); ok {
								contentStr = s
							} else {
								contentStr = stringify(trContent)
							}
							toolResults = append(toolResults, map[string]interface{}{
								"role":         "tool",
								"tool_call_id": toolUseID,
								"content":      contentStr,
							})
						} else if blockType == "image" {
							src, _ := block["source"].(map[string]interface{})
							mediaType, _ := src["media_type"].(string)
							data, _ := src["data"].(string)
							url := fmt.Sprintf("data:%s;base64,%s", mediaType, data)
							otherParts = append(otherParts, map[string]interface{}{
								"type": "image_url",
								"image_url": map[string]interface{}{
									"url": url,
								},
							})
						} else {
							otherParts = append(otherParts, c)
						}
					}

					// Emit tool_result messages
					for _, tr := range toolResults {
						messages = append(messages, tr)
					}

					// Emit remaining user content
					if len(otherParts) > 0 {
						messages = append(messages, map[string]interface{}{
							"role":    "user",
							"content": otherParts,
						})
					}
				} else {
					messages = append(messages, msg)
				}

			default:
				// Mid-conversation system messages (e.g. Claude Code's
				// <system-reminder> injections) must not become role:system here:
				// many OpenAI chat templates (Claude-Mythos on llama.cpp, etc.)
				// require the system message to be strictly first and raise
				// "System message must be at the beginning". Render them as user
				// messages so they stay in position without violating that.
				if role == "system" {
					messages = append(messages, map[string]interface{}{
						"role":    "user",
						"content": anthropicSystemText(content),
					})
					continue
				}
				messages = append(messages, msg)
			}
		}
	}

	result["messages"] = messages

	// Convert tools
	if toolsRaw, ok := body["tools"].([]interface{}); ok {
		var newTools []interface{}
		for _, t := range toolsRaw {
			tool, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			newTool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name": tool["name"],
				},
			}
			if desc, ok := tool["description"].(string); ok {
				newTool["function"].(map[string]interface{})["description"] = desc
			}
			if schema, ok := tool["input_schema"].(map[string]interface{}); ok {
				newTool["function"].(map[string]interface{})["parameters"] = schema
			}
			newTools = append(newTools, newTool)
		}
		if len(newTools) > 0 {
			result["tools"] = newTools
		}
	}

	// Convert tool_choice
	if tcRaw, ok := body["tool_choice"]; ok {
		switch tc := tcRaw.(type) {
		case map[string]interface{}:
			tcType, _ := tc["type"].(string)
			switch tcType {
			case "auto":
				result["tool_choice"] = "auto"
			case "any":
				result["tool_choice"] = "required"
			case "none":
				result["tool_choice"] = "none"
			case "tool":
				if name, ok := tc["name"].(string); ok {
					result["tool_choice"] = map[string]interface{}{
						"type": "function",
						"function": map[string]interface{}{
							"name": name,
						},
					}
				}
			}
		case string:
			switch tc {
			case "auto":
				result["tool_choice"] = "auto"
			case "any":
				result["tool_choice"] = "required"
			case "none":
				result["tool_choice"] = "none"
			default:
				result["tool_choice"] = tc
			}
		}
	}

	return result, nil
}

// TranslateOpenAIResponseToAnthropic converts an OpenAI Chat Completions response
// to Anthropic Messages API format.
func TranslateOpenAIResponseToAnthropic(data []byte) ([]byte, error) {
	var openai struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			Index        int    `json:"index"`
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role      string      `json:"role"`
				Content   interface{} `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &openai); err != nil {
		return data, err
	}

	if len(openai.Choices) == 0 {
		// Likely an error response — return as-is so caller can propagate it.
		return data, nil
	}

	choice := openai.Choices[0]
	msg := choice.Message

	var content []map[string]interface{}

	// Text content
	if text, ok := msg.Content.(string); ok && text != "" {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": text,
		})
	} else if contentArr, ok := msg.Content.([]interface{}); ok {
		for _, c := range contentArr {
			if block, ok := c.(map[string]interface{}); ok {
				content = append(content, block)
			}
		}
	}

	// Tool calls → tool_use blocks
	for _, tc := range msg.ToolCalls {
		input := map[string]interface{}{}
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &input)
		}
		content = append(content, map[string]interface{}{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Function.Name,
			"input": input,
		})
	}

	// Reasoning/thinking
	if msg.Reasoning != "" {
		content = append(content, map[string]interface{}{
			"type":     "thinking",
			"thinking": msg.Reasoning,
		})
	}

	if len(content) == 0 {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	stopReason := "end_turn"
	switch choice.FinishReason {
	case "tool_calls", "function_call":
		stopReason = "tool_use"
	case "length":
		stopReason = "max_tokens"
	case "stop":
		stopReason = "end_turn"
	}

	anthropic := map[string]interface{}{
		"id":          msgOpenAIIDToAnthropic(openai.ID),
		"type":        "message",
		"role":        "assistant",
		"model":       openai.Model,
		"content":     content,
		"stop_reason": stopReason,
		"usage": map[string]interface{}{
			"input_tokens":  openai.Usage.PromptTokens,
			"output_tokens": openai.Usage.CompletionTokens,
		},
	}

	return json.Marshal(anthropic)
}

func msgOpenAIIDToAnthropic(id string) string {
	if strings.HasPrefix(id, "chatcmpl-") {
		return strings.TrimPrefix(id, "chatcmpl-")
	}
	return id
}
