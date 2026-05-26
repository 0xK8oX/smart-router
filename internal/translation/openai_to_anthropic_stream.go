package translation

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var streamIDCounter uint64

func generateStreamID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatUint(atomic.AddUint64(&streamIDCounter, 1), 10)
}

// OpenAIToAnthropicStream converts an OpenAI-format SSE stream to Anthropic-format SSE.
func OpenAIToAnthropicStream(reader io.Reader) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(reader)
		const maxCapacity = 512 * 1024 // 512KB
		scanBuf := make([]byte, 4096)
		scanner.Buffer(scanBuf, maxCapacity)
		var msgID string
		var model string
		messageStarted := false
		messageStopped := false
		textBlockStarted := false
		thinkingBlockStarted := false
		toolCalls := make(map[int]*toolCallState)

		emitMessageStart := func() {
			if messageStarted {
				return
			}
			if msgID == "" {
				msgID = "msg_" + generateStreamID()
			}
			if model == "" {
				model = "unknown"
			}
			msgStart := map[string]interface{}{
				"type": "message_start",
				"message": map[string]interface{}{
					"id":    msgID,
					"type":  "message",
					"role":  "assistant",
					"model": model,
					"usage": map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
				},
			}
			b, err := json.Marshal(msgStart)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write([]byte("event: message_start\ndata: " + string(b) + "\n\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
			messageStarted = true
		}

		emitContentBlockStart := func(idx int, blockType string, block map[string]interface{}) {
			event := map[string]interface{}{
				"type":          "content_block_start",
				"index":         idx,
				"content_block": block,
			}
			b, err := json.Marshal(event)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write([]byte("event: content_block_start\ndata: " + string(b) + "\n\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
		}

		emitContentBlockDelta := func(idx int, delta map[string]interface{}) {
			event := map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": delta,
			}
			b, err := json.Marshal(event)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write([]byte("event: content_block_delta\ndata: " + string(b) + "\n\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
		}

		emitContentBlockStop := func(idx int) {
			event := map[string]interface{}{
				"type":  "content_block_stop",
				"index": idx,
			}
			b, err := json.Marshal(event)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write([]byte("event: content_block_stop\ndata: " + string(b) + "\n\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
		}

		emitMessageDelta := func(stopReason string, usage map[string]interface{}) {
			msgDelta := map[string]interface{}{
				"type": "message_delta",
				"delta": map[string]interface{}{
					"stop_reason": stopReason,
				},
			}
			if usage != nil {
				msgDelta["usage"] = usage
			} else {
				msgDelta["usage"] = map[string]interface{}{"output_tokens": 0}
			}
			b, err := json.Marshal(msgDelta)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write([]byte("event: message_delta\ndata: " + string(b) + "\n\n")); err != nil {
				pw.CloseWithError(err)
				return
			}
		}

		closeOpenBlocks := func() {
			if textBlockStarted {
				emitContentBlockStop(0)
				textBlockStarted = false
			}
			if thinkingBlockStarted {
				emitContentBlockStop(0)
				thinkingBlockStarted = false
			}
			for idx := range toolCalls {
				emitContentBlockStop(idx)
			}
			toolCalls = make(map[int]*toolCallState)
		}

		emitMessageStop := func() {
			if messageStarted && !messageStopped {
				closeOpenBlocks()
				if _, err := pw.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")); err != nil {
					pw.CloseWithError(err)
					return
				}
				messageStopped = true
			}
		}

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
				emitMessageStop()
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			if id, ok := event["id"].(string); ok && id != "" {
				msgID = id
			}
			if m, ok := event["model"].(string); ok && m != "" {
				model = m
			}

			choices, _ := event["choices"].([]interface{})
			if len(choices) == 0 {
				if errObj, ok := event["error"].(map[string]interface{}); ok {
					errMsg := "unknown stream error"
					if msg, ok := errObj["message"].(string); ok {
						errMsg = msg
					} else if errType, ok := errObj["type"].(string); ok {
						errMsg = errType
					}
					chunk := map[string]interface{}{
						"type": "error",
						"error": map[string]interface{}{
							"type":    "stream_error",
							"message": errMsg,
						},
					}
					b, err := json.Marshal(chunk)
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					if _, err := pw.Write([]byte("event: error\ndata: " + string(b) + "\n\n")); err != nil {
						pw.CloseWithError(err)
						return
					}
				}
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					emitMessageStart()
					translatedUsage := make(map[string]interface{})
					if v, ok := usage["prompt_tokens"]; ok {
						translatedUsage["input_tokens"] = v
					} else if v, ok := usage["input_tokens"]; ok {
						translatedUsage["input_tokens"] = v
					}
					if v, ok := usage["completion_tokens"]; ok {
						translatedUsage["output_tokens"] = v
					} else if v, ok := usage["output_tokens"]; ok {
						translatedUsage["output_tokens"] = v
					}
					chunk := map[string]interface{}{
						"type":  "message_delta",
						"usage": translatedUsage,
					}
					b, err := json.Marshal(chunk)
					if err != nil {
						pw.CloseWithError(err)
						return
					}
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

			// Handle tool_calls
			if toolCallsRaw, ok := delta["tool_calls"].([]interface{}); ok && len(toolCallsRaw) > 0 {
				emitMessageStart()
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

					idx := int(tcIndex)
					if tcID != "" && (toolCalls[idx] == nil || toolCalls[idx].id != tcID) {
						toolCalls[idx] = &toolCallState{
							id:    tcID,
							name:  tcName,
							index: idx,
						}
						emitContentBlockStart(idx, "tool_use", map[string]interface{}{
							"type":  "tool_use",
							"id":    tcID,
							"name":  tcName,
							"input": map[string]interface{}{},
						})
					}

					if tcArgs != "" && toolCalls[idx] != nil {
						emitContentBlockDelta(idx, map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": tcArgs,
						})
					}
				}
			}

			// Handle text content
			if text, ok := delta["content"].(string); ok && text != "" {
				emitMessageStart()
				if !textBlockStarted {
					emitContentBlockStart(0, "text", map[string]interface{}{
						"type": "text",
						"text": "",
					})
					textBlockStarted = true
				}
				emitContentBlockDelta(0, map[string]interface{}{
					"type": "text_delta",
					"text": text,
				})
			}

			// Handle reasoning
			if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
				emitMessageStart()
				if !thinkingBlockStarted {
					emitContentBlockStart(0, "thinking", map[string]interface{}{
						"type":     "thinking",
						"thinking": "",
					})
					thinkingBlockStarted = true
				}
				emitContentBlockDelta(0, map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": reasoning,
				})
			}

			// Handle finish_reason
			if finishReason != "" {
				emitMessageStart()
				stopReason := finishReason
				switch finishReason {
				case "tool_calls":
					stopReason = "tool_use"
				case "function_call":
					stopReason = "tool_use"
				case "length":
					stopReason = "max_tokens"
				case "stop":
					stopReason = "end_turn"
				}
				closeOpenBlocks()
				var translatedUsage map[string]interface{}
				if usage, ok := event["usage"].(map[string]interface{}); ok {
					translatedUsage = make(map[string]interface{})
					if v, ok := usage["prompt_tokens"]; ok {
						translatedUsage["input_tokens"] = v
					} else if v, ok := usage["input_tokens"]; ok {
						translatedUsage["input_tokens"] = v
					}
					if v, ok := usage["completion_tokens"]; ok {
						translatedUsage["output_tokens"] = v
					} else if v, ok := usage["output_tokens"]; ok {
						translatedUsage["output_tokens"] = v
					}
				}
				emitMessageDelta(stopReason, translatedUsage)
			}
		}

		if err := scanner.Err(); err != nil {
			pw.CloseWithError(err)
			return
		}
		// Stream ended without [DONE] — some proxies omit it.
		emitMessageStop()
	}()

	return pr
}
