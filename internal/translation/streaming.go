package translation

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// SSETranslator converts SSE streams between Anthropic and OpenAI formats.
func SSETranslator(reader io.Reader, fromFormat, toFormat string) io.Reader {
	if fromFormat == toFormat {
		return reader
	}
	if fromFormat == "anthropic" && toFormat == "openai" {
		return anthropicToOpenAIStream(reader)
	}
	return reader
}

// toolCallState tracks an in-progress tool_use block so we can emit OpenAI-style
// streaming tool_call deltas.
type toolCallState struct {
	id        string
	name      string
	index     int
	started   bool // true once we've emitted the id/name chunk
}

func anthropicToOpenAIStream(reader io.Reader) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(reader)
		var id string
		var model string
		roleSent := false
		toolCalls := make(map[int]*toolCallState)

		for scanner.Scan() {
			line := scanner.Text()

			if strings.TrimSpace(line) == "" {
				continue
			}

			if !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}

			// Check for ping events (no event: prefix, just data: {})
			if data == "{}" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			eventType, _ := event["type"].(string)

			switch eventType {
			case "message_start":
				msg, _ := event["message"].(map[string]interface{})
				if msg != nil {
					if v, ok := msg["id"].(string); ok {
						id = v
					}
					if v, ok := msg["model"].(string); ok {
						model = v
					}
				}

			case "content_block_start":
				idx, _ := event["index"].(float64)
				contentBlock, _ := event["content_block"].(map[string]interface{})
				if contentBlock == nil {
					continue
				}
				blockType, _ := contentBlock["type"].(string)
				if blockType == "tool_use" {
					toolID, _ := contentBlock["id"].(string)
					toolName, _ := contentBlock["name"].(string)
					toolCalls[int(idx)] = &toolCallState{
						id:    toolID,
						name:  toolName,
						index: int(idx),
					}
					chunk := map[string]interface{}{
						"id":      id,
						"object":  "chat.completion.chunk",
						"model":   model,
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]interface{}{
									"tool_calls": []map[string]interface{}{
										{
											"index": int(idx),
											"id":    toolID,
											"type":  "function",
											"function": map[string]interface{}{
												"name":      toolName,
												"arguments": "",
											},
										},
									},
								},
							},
						},
					}
					if !roleSent {
						chunk["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["role"] = "assistant"
						roleSent = true
					}
					b, _ := json.Marshal(chunk)
					if _, err := pw.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
						pw.CloseWithError(err)
						return
					}
				}

			case "content_block_delta":
				idx, _ := event["index"].(float64)
				delta, _ := event["delta"].(map[string]interface{})
				if delta == nil {
					continue
				}

				// Tool argument delta
				if partialJSON, ok := delta["partial_json"].(string); ok {
					tc := toolCalls[int(idx)]
					if tc == nil {
						continue
					}
					chunk := map[string]interface{}{
						"id":      id,
						"object":  "chat.completion.chunk",
						"model":   model,
						"choices": []map[string]interface{}{
							{
								"index": 0,
								"delta": map[string]interface{}{
									"tool_calls": []map[string]interface{}{
										{
											"index": tc.index,
											"function": map[string]interface{}{
												"arguments": partialJSON,
											},
										},
									},
								},
							},
						},
					}
					if !roleSent {
						chunk["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["role"] = "assistant"
						roleSent = true
					}
					b, _ := json.Marshal(chunk)
					if _, err := pw.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
						pw.CloseWithError(err)
						return
					}
					continue
				}

				// Text delta
				text, _ := delta["text"].(string)
				if text == "" {
					continue
				}

				chunk := map[string]interface{}{
					"id":      id,
					"object":  "chat.completion.chunk",
					"model":   model,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"content": text,
							},
						},
					},
				}
				if !roleSent {
					chunk["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["role"] = "assistant"
					roleSent = true
				}

				b, _ := json.Marshal(chunk)
				if _, err := pw.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}

			case "thinking_delta":
				delta, _ := event["delta"].(map[string]interface{})
				if delta == nil {
					continue
				}
				thinking, _ := delta["thinking"].(string)
				if thinking == "" {
					continue
				}
				chunk := map[string]interface{}{
					"id":      id,
					"object":  "chat.completion.chunk",
					"model":   model,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"reasoning": thinking,
							},
						},
					},
				}
				if !roleSent {
					chunk["choices"].([]map[string]interface{})[0]["delta"].(map[string]interface{})["role"] = "assistant"
					roleSent = true
				}
				b, _ := json.Marshal(chunk)
				if _, err := pw.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}

			case "content_block_stop":
				idx, _ := event["index"].(float64)
				delete(toolCalls, int(idx))

			case "message_delta":
				msgDelta, _ := event["delta"].(map[string]interface{})
				usage, _ := event["usage"].(map[string]interface{})
				chunk := map[string]interface{}{
					"id":      id,
					"object":  "chat.completion.chunk",
					"model":   model,
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{},
							"finish_reason": msgDelta["stop_reason"],
						},
					},
				}
				if usage != nil {
					chunk["usage"] = usage
				}
				b, _ := json.Marshal(chunk)
				if _, err := pw.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}

			case "message_stop":
				if _, err := pw.Write([]byte("data: [DONE]\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			pw.CloseWithError(err)
		}
	}()

	return pr
}

// IsStreamingResponse checks if response headers indicate SSE streaming.
func IsStreamingResponse(header http.Header) bool {
	return strings.Contains(header.Get("Content-Type"), "text/event-stream")
}
