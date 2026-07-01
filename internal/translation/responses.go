package translation

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// randID returns n bytes of random hex (2n hex characters).
func randID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ResponsesRequestToChat converts an OpenAI Responses API request body
// (input items, instructions, flat tools) into an OpenAI Chat Completions
// request body (messages, nested tools). The model field is copied through
// verbatim; the caller strips any plan prefix afterwards.
//
// Dropped (no chat equivalent): store, previous_response_id, include,
// reasoning, metadata. We are stateless, so server-side response storage is
// unsupported by design — callers must resend the full conversation.
func ResponsesRequestToChat(body map[string]interface{}) (map[string]interface{}, error) {
	out := map[string]interface{}{}

	if m, ok := body["model"]; ok {
		out["model"] = m
	}

	var messages []interface{}
	var systemParts []string

	// instructions -> system content (merged into a single leading message below).
	if instr, ok := body["instructions"].(string); ok && instr != "" {
		systemParts = append(systemParts, instr)
	}

	// input: string shorthand | []input item | {input: {...}} wrapper
	input := body["input"]
	switch in := input.(type) {
	case string:
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": in,
		})
	case []interface{}:
		// Consecutive function_call items coalesce into one assistant message
		// with multiple tool_calls (the OpenAI Chat ordering invariant).
		// NOTE: slices must be []interface{} (not []map) so the downstream
		// openai->anthropic converters' type assertions (body["tools"].([]interface{}))
		// succeed — otherwise tools/tool_calls get silently dropped.
		var pendingToolCalls []interface{}
		flushToolCalls := func() {
			if len(pendingToolCalls) == 0 {
				return
			}
			messages = append(messages, map[string]interface{}{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": pendingToolCalls,
			})
			pendingToolCalls = nil
		}
		for _, raw := range in {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			switch item["type"] {
			case "message":
				flushToolCalls()
				role, _ := item["role"].(string)
				if role == "" {
					role = "user"
				}
				// System/developer instructions merge into ONE leading system
				// message. Many chat templates (e.g. Claude-Mythos on llama.cpp)
				// reject a system message that isn't strictly first, and clients
				// like Codex send instructions + a developer item separately.
				if role == "system" || role == "developer" {
					if c, ok := responsesContentToChat(item["content"]).(string); ok && c != "" {
						systemParts = append(systemParts, c)
					}
					continue
				}
				messages = append(messages, map[string]interface{}{
					"role":    role,
					"content": responsesContentToChat(item["content"]),
				})
			case "function_call":
				id, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				pendingToolCalls = append(pendingToolCalls, map[string]interface{}{
					"id":   id,
					"type": "function",
					"function": map[string]interface{}{
						"name":      name,
						"arguments": args,
					},
				})
			case "function_call_output":
				flushToolCalls()
				id, _ := item["call_id"].(string)
				messages = append(messages, map[string]interface{}{
					"role":         "tool",
					"tool_call_id": id,
					"content":      stringify(item["output"]),
				})
			case "reasoning":
				// No first-class chat equivalent — skip.
			}
		}
		flushToolCalls()
	}

	// Emit a single leading system message from all system/developer/instructions
	// content, so downstream chat templates that require system-first are satisfied.
	if len(systemParts) > 0 {
		sysMsg := map[string]interface{}{
			"role":    "system",
			"content": strings.Join(systemParts, "\n\n"),
		}
		messages = append([]interface{}{sysMsg}, messages...)
	}

	out["messages"] = messages

	// tools: Responses flat {type:function,name,...} -> chat nested
	if toolsRaw, ok := body["tools"].([]interface{}); ok {
		var chatTools []interface{}
		for _, t := range toolsRaw {
			tm, ok := t.(map[string]interface{})
			if !ok || tm["type"] != "function" {
				continue
			}
			fn := map[string]interface{}{"name": tm["name"]}
			if d, ok := tm["description"]; ok {
				fn["description"] = d
			}
			if p, ok := tm["parameters"]; ok {
				fn["parameters"] = p
			} else {
				fn["parameters"] = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			}
			chatTools = append(chatTools, map[string]interface{}{
				"type":     "function",
				"function": fn,
			})
		}
		if len(chatTools) > 0 {
			out["tools"] = chatTools
		}
	}

	if tc, ok := body["tool_choice"]; ok {
		out["tool_choice"] = responsesToolChoiceToChat(tc)
	}

	if v, ok := body["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}
	if v, ok := body["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := body["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := body["stream"]; ok {
		out["stream"] = v
	}

	return out, nil
}

// responsesContentToChat flattens a Responses message content field (string or
// []content block) into a chat message content (string). Image blocks are
// dropped for now.
func responsesContentToChat(c interface{}) interface{} {
	if s, ok := c.(string); ok {
		return s
	}
	blocks, ok := c.([]interface{})
	if !ok {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		bm, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		switch bm["type"] {
		case "input_text", "output_text", "text":
			if t, ok := bm["text"].(string); ok {
				texts = append(texts, t)
			}
		}
	}
	if len(texts) == 1 {
		return texts[0]
	}
	return strings.Join(texts, "\n")
}

func responsesToolChoiceToChat(tc interface{}) interface{} {
	m, ok := tc.(map[string]interface{})
	if !ok {
		return tc // already a string like "auto"
	}
	switch m["type"] {
	case "auto", "none":
		return m["type"]
	case "required", "any":
		return "required"
	case "function":
		name, _ := m["name"].(string)
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": name,
			},
		}
	}
	return "auto"
}

// ChatCompletionToResponses converts an OpenAI Chat Completions response body
// into an OpenAI Responses API response body. The input must already be in
// chat-completions format (translate from the provider format first if needed).
func ChatCompletionToResponses(data []byte) ([]byte, error) {
	var chat struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role      string          `json:"role"`
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &chat); err != nil {
		return data, err
	}

	status := "completed"
	contentText := ""
	var output []map[string]interface{}

	if len(chat.Choices) > 0 {
		ch := chat.Choices[0]
		status = mapChatFinishToResponses(ch.FinishReason)
		contentText = chatContentToText(ch.Message.Content)

		if contentText != "" {
			output = append(output, map[string]interface{}{
				"type":   "message",
				"id":     "msg_" + randID(12),
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{
						"type":        "output_text",
						"text":        contentText,
						"annotations": []interface{}{},
					},
				},
			})
		}

		for _, tc := range ch.Message.ToolCalls {
			id := tc.ID
			if id == "" {
				id = "call_" + randID(12)
			}
			output = append(output, map[string]interface{}{
				"type":      "function_call",
				"id":        id,
				"call_id":   id,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"status":    "completed",
			})
		}
	}

	resp := map[string]interface{}{
		"id":         "resp_" + randID(12),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     status,
		"model":      chat.Model,
		"output":     output,
	}
	if chat.Usage.PromptTokens != 0 || chat.Usage.CompletionTokens != 0 {
		resp["usage"] = map[string]interface{}{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
			"total_tokens":  chat.Usage.TotalTokens,
		}
	}

	return json.Marshal(resp)
}

// chatContentToText extracts plain text from a chat message content field
// (string, null, or array of content blocks).
func chatContentToText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []map[string]interface{}
	if json.Unmarshal(raw, &arr) == nil {
		var texts []string
		for _, b := range arr {
			if t, ok := b["text"].(string); ok {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

func mapChatFinishToResponses(fr string) string {
	switch fr {
	case "length", "content_filter":
		return "incomplete"
	default:
		return "completed"
	}
}
