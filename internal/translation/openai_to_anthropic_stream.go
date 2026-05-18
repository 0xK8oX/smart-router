package translation

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// OpenAIToAnthropicStream converts an OpenAI-format SSE stream to Anthropic-format SSE.
func OpenAIToAnthropicStream(reader io.Reader) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(reader)
		var msgID string
		var model string
		roleSent := false
		var currentTool *toolCallState

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

			if data == "[DONE]" {
				if _, err := pw.Write([]byte("event: message_stop\ndata: {}\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			// Extract id and model from first chunk if present
			if id, ok := event["id"].(string); ok && id != "" {
				msgID = id
			}
			if m, ok := event["model"].(string); ok && m != "" {
				model = m
			}

			choices, _ := event["choices"].([]interface{})
			if len(choices) == 0 {
				// Check for usage-only chunk
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					chunk := map[string]interface{}{
						"type":       "message_delta",
						"usage":      usage,
					}
					b, _ := json.Marshal(chunk)
					if _, err := pw.Write([]byte("event: message_delta\ndata: " + string(b) + "\n\n")); err != nil {
						pw.CloseWithError(err)
						return
					}
				}
				continue
			}

			choice, _ := choices[0].(map[string]interface{})
			delta, _ := choice["delta"].(map[string]interface{})
			finishReason, _ := choice["finish_reason"].(string)

			// Emit message_start on first chunk with role
			if deltaRole, ok := delta["role"].(string); ok && deltaRole == "assistant" && !roleSent {
				msgStart := map[string]interface{}{
					"type": "message_start",
					"message": map[string]interface{}{
						"id":     msgID,
						"type":   "message",
						"role":   "assistant",
						"model":  model,
						"usage":  map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
					},
				}
				b, _ := json.Marshal(msgStart)
				if _, err := pw.Write([]byte("event: message_start\ndata: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
				roleSent = true
			}

			// Handle tool_calls
			if toolCallsRaw, ok := delta["tool_calls"].([]interface{}); ok && len(toolCallsRaw) > 0 {
				for _, tcRaw := range toolCallsRaw {
					tc, ok := tcRaw.(map[string]interface{})
					if !ok {
						continue
					}
					tcIndex, _ := tc["index"].(float64)
					tcID, _ := tc["id"].(string)
					fn, ok := tc["function"].(map[string]interface{})
					if !ok {
						continue
					}
					tcName, _ := fn["name"].(string)
					tcArgs, _ := fn["arguments"].(string)

					if tcID != "" && (currentTool == nil || currentTool.id != tcID) {
						// New tool call - emit content_block_start
						currentTool = &toolCallState{
							id:    tcID,
							name:  tcName,
							index: int(tcIndex),
						}
						blockStart := map[string]interface{}{
							"type": "content_block_start",
							"index": int(tcIndex),
							"content_block": map[string]interface{}{
								"type": "tool_use",
								"id":   tcID,
								"name": tcName,
								"input": map[string]interface{}{},
							},
						}
						b, _ := json.Marshal(blockStart)
						if _, err := pw.Write([]byte("event: content_block_start\ndata: " + string(b) + "\n\n")); err != nil {
							pw.CloseWithError(err)
							return
						}
					}

					if tcArgs != "" && currentTool != nil {
						// Emit input_json_delta
						deltaEvent := map[string]interface{}{
							"type": "content_block_delta",
							"index": currentTool.index,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": tcArgs,
							},
						}
						b, _ := json.Marshal(deltaEvent)
						if _, err := pw.Write([]byte("event: content_block_delta\ndata: " + string(b) + "\n\n")); err != nil {
							pw.CloseWithError(err)
							return
						}
					}
				}
				continue
			}

			// Handle text content
			if text, ok := delta["content"].(string); ok && text != "" {
				deltaEvent := map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type": "text_delta",
						"text": text,
					},
				}
				b, _ := json.Marshal(deltaEvent)
				if _, err := pw.Write([]byte("event: content_block_delta\ndata: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
			}

			// Handle reasoning
			if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
				deltaEvent := map[string]interface{}{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]interface{}{
						"type":     "thinking_delta",
						"thinking": reasoning,
					},
				}
				b, _ := json.Marshal(deltaEvent)
				if _, err := pw.Write([]byte("event: content_block_delta\ndata: " + string(b) + "\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
			}

			// Handle finish_reason
			if finishReason != "" {
				stopReason := finishReason
				switch finishReason {
				case "tool_calls":
					stopReason = "tool_use"
				case "length":
					stopReason = "max_tokens"
				case "stop":
					stopReason = "end_turn"
				}
				msgDelta := map[string]interface{}{
					"type": "message_delta",
					"delta": map[string]interface{}{
						"stop_reason": stopReason,
					},
				}
				b, _ := json.Marshal(msgDelta)
				if _, err := pw.Write([]byte("event: message_delta\ndata: " + string(b) + "\n\n")); err != nil {
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
