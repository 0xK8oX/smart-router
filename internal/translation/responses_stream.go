package translation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatSSEToResponses consumes an OpenAI Chat Completions SSE stream and emits
// the equivalent OpenAI Responses API SSE stream in real time. It is the
// streaming counterpart of ChatCompletionToResponses: each upstream token
// becomes a response.output_text.delta event as it arrives, so clients like
// Codex see a properly-paced incremental stream rather than a burst at the
// end of generation.
func ChatSSEToResponses(reader io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 512*1024)

		var (
			respID, model   string
			createdAt       int64
			created         bool
			finishReason    string
			rawUsage        map[string]interface{}
			nextOutputIndex int
			seq             int
			msgOutputIndex  = -1
			msgItemID       string
			msgStarted      bool
			msgFullText     strings.Builder
			toolItems       = map[int]*tcItem{}
			toolOrder       []int
		)

		writeEvent := func(event string, data map[string]interface{}) {
			// OpenAI's Responses streaming protocol carries the event type and a
			// monotonic sequence number INSIDE the data JSON; the `event:` line is
			// informational. Clients (Codex) dispatch on data["type"], so it must
			// be present or the stream is treated as never completing.
			data["type"] = event
			data["sequence_number"] = seq
			seq++
			b, _ := json.Marshal(data)
			_, _ = pw.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
		}

		ensureCreated := func() {
			if created {
				return
			}
			created = true
			createdAt = time.Now().Unix()
			if respID == "" {
				respID = "resp_" + randID(12)
			}
			skeleton := map[string]interface{}{
				"id":         respID,
				"object":     "response",
				"created_at": createdAt,
				"model":      model,
				"status":     "in_progress",
				"output":     []interface{}{},
			}
			writeEvent("response.created", map[string]interface{}{"response": skeleton})
		}

		emitCompletion := func() {
			var output []map[string]interface{}
			if msgStarted {
				output = append(output, map[string]interface{}{
					"type": "message", "id": msgItemID, "role": "assistant", "status": "completed",
					"content": []map[string]interface{}{
						{"type": "output_text", "text": msgFullText.String(), "annotations": []interface{}{}},
					},
				})
			}
			for _, idx := range toolOrder {
				tc := toolItems[idx]
				output = append(output, map[string]interface{}{
					"type": "function_call", "id": tc.id, "call_id": tc.id,
					"name": tc.name, "arguments": tc.fullArgs.String(), "status": "completed",
				})
			}
			if output == nil {
				output = []map[string]interface{}{}
			}
			resp := map[string]interface{}{
				"id": respID, "object": "response", "created_at": createdAt,
				"model": model, "status": mapChatFinishToResponses(finishReason),
				"output": output,
			}
			if rawUsage != nil {
				u := map[string]interface{}{}
				if v, ok := rawUsage["prompt_tokens"]; ok {
					u["input_tokens"] = v
				} else if v, ok := rawUsage["input_tokens"]; ok {
					u["input_tokens"] = v
				}
				if v, ok := rawUsage["completion_tokens"]; ok {
					u["output_tokens"] = v
				} else if v, ok := rawUsage["output_tokens"]; ok {
					u["output_tokens"] = v
				}
				if v, ok := rawUsage["total_tokens"]; ok {
					u["total_tokens"] = v
				} else {
					u["total_tokens"] = u["input_tokens"]
				}
				resp["usage"] = u
			}
			writeEvent("response.completed", map[string]interface{}{"response": resp})
		}

		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				ensureCreated()
				if msgStarted {
					writeEvent("response.output_text.done", map[string]interface{}{
						"item_id": msgItemID, "output_index": msgOutputIndex,
						"content_index": 0, "text": msgFullText.String(),
					})
					writeEvent("response.output_item.done", map[string]interface{}{
						"output_index": msgOutputIndex,
						"item": map[string]interface{}{
							"type": "message", "id": msgItemID, "role": "assistant", "status": "completed",
							"content": []map[string]interface{}{
								{"type": "output_text", "text": msgFullText.String(), "annotations": []interface{}{}},
							},
						},
					})
				}
				for _, idx := range toolOrder {
					tc := toolItems[idx]
					writeEvent("response.function_call_arguments.done", map[string]interface{}{
						"item_id": tc.id, "output_index": tc.outputIndex,
						"arguments": tc.fullArgs.String(),
					})
					writeEvent("response.output_item.done", map[string]interface{}{
						"output_index": tc.outputIndex,
						"item": map[string]interface{}{
							"type": "function_call", "id": tc.id, "call_id": tc.id,
							"name": tc.name, "arguments": tc.fullArgs.String(), "status": "completed",
						},
					})
				}
				emitCompletion()
				return
			}

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			// The first non-empty model we see becomes the Responses model.
			if v, ok := chunk["model"].(string); ok && model == "" {
				model = v
			}
			if u, ok := chunk["usage"].(map[string]interface{}); ok {
				rawUsage = u
			}

			choices, _ := chunk["choices"].([]interface{})
			if len(choices) == 0 {
				// Could be a standalone usage chunk; already captured above.
				continue
			}
			ch, _ := choices[0].(map[string]interface{})
			delta, _ := ch["delta"].(map[string]interface{})
			if fr, ok := ch["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}

			ensureCreated()

			// Text delta
			if delta != nil {
				if text, ok := delta["content"].(string); ok && text != "" {
					if !msgStarted {
						msgStarted = true
						msgItemID = "msg_" + randID(12)
						msgOutputIndex = nextOutputIndex
						nextOutputIndex++
						writeEvent("response.output_item.added", map[string]interface{}{
							"output_index": msgOutputIndex,
							"item": map[string]interface{}{
								"type": "message", "id": msgItemID, "role": "assistant",
								"status": "in_progress", "content": []interface{}{},
							},
						})
					}
					msgFullText.WriteString(text)
					writeEvent("response.output_text.delta", map[string]interface{}{
						"item_id": msgItemID, "output_index": msgOutputIndex,
						"content_index": 0, "delta": text,
					})
				}

				// Tool-call deltas (indexed)
				if tcs, ok := delta["tool_calls"].([]interface{}); ok {
					for _, raw := range tcs {
						tcm, ok := raw.(map[string]interface{})
						if !ok {
							continue
						}
						var chatIdx int
						if v, ok := tcm["index"].(float64); ok {
							chatIdx = int(v)
						}
						tc, exists := toolItems[chatIdx]
						if !exists {
							tc = &tcItem{}
							toolItems[chatIdx] = tc
							toolOrder = append(toolOrder, chatIdx)
						}
						// First fragment carries id+name and signals item start.
						if id, ok := tcm["id"].(string); ok && id != "" {
							tc.id = id
						}
						if fn, ok := tcm["function"].(map[string]interface{}); ok {
							if name, ok := fn["name"].(string); ok && name != "" {
								tc.name = name
							}
							if args, ok := fn["arguments"].(string); ok && args != "" {
								if !tc.started {
									tc.started = true
									tc.outputIndex = nextOutputIndex
									nextOutputIndex++
									if tc.id == "" {
										tc.id = "call_" + randID(12)
									}
									writeEvent("response.output_item.added", map[string]interface{}{
										"output_index": tc.outputIndex,
										"item": map[string]interface{}{
											"type": "function_call", "id": tc.id, "call_id": tc.id,
											"name": tc.name, "arguments": "", "status": "in_progress",
										},
									})
								}
								tc.fullArgs.WriteString(args)
								writeEvent("response.function_call_arguments.delta", map[string]interface{}{
									"item_id": tc.id, "output_index": tc.outputIndex, "delta": args,
								})
							}
						}
					}
				}
			}
		}
		// Stream ended without [DONE]: emit completion based on what we have.
		ensureCreated()
		if msgStarted {
			writeEvent("response.output_text.done", map[string]interface{}{
				"item_id": msgItemID, "output_index": msgOutputIndex,
				"content_index": 0, "text": msgFullText.String(),
			})
			writeEvent("response.output_item.done", map[string]interface{}{
				"output_index": msgOutputIndex,
				"item": map[string]interface{}{
					"type": "message", "id": msgItemID, "role": "assistant", "status": "completed",
					"content": []map[string]interface{}{
						{"type": "output_text", "text": msgFullText.String(), "annotations": []interface{}{}},
					},
				},
			})
		}
		for _, idx := range toolOrder {
			tc := toolItems[idx]
			writeEvent("response.function_call_arguments.done", map[string]interface{}{
				"item_id": tc.id, "output_index": tc.outputIndex,
				"arguments": tc.fullArgs.String(),
			})
			writeEvent("response.output_item.done", map[string]interface{}{
				"output_index": tc.outputIndex,
				"item": map[string]interface{}{
					"type": "function_call", "id": tc.id, "call_id": tc.id,
					"name": tc.name, "arguments": tc.fullArgs.String(), "status": "completed",
				},
			})
		}
		emitCompletion()
	}()
	return pr
}

type tcItem struct {
	outputIndex int
	id, name    string
	fullArgs    strings.Builder
	started     bool
}

// WriteResponsesSSE synthesizes the OpenAI Responses SSE event stream from a
// complete Responses object (the buffered result of ChatCompletionToResponses).
// It is used as a fallback when the upstream did not actually stream.
func WriteResponsesSSE(w http.ResponseWriter, responsesBytes []byte) error {
	var resp map[string]interface{}
	if err := json.Unmarshal(responsesBytes, &resp); err != nil {
		return err
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	seq := 0
	writeEvent := func(event string, data map[string]interface{}) {
		data["type"] = event
		data["sequence_number"] = seq
		seq++
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	created := cloneMap(resp)
	created["status"] = "in_progress"
	created["output"] = []interface{}{}
	writeEvent("response.created", map[string]interface{}{"response": created})

	output, _ := resp["output"].([]interface{})
	for i, itemRaw := range output {
		item, ok := itemRaw.(map[string]interface{})
		if !ok {
			continue
		}
		outputIndex := i
		itemID, _ := item["id"].(string)

		writeEvent("response.output_item.added", map[string]interface{}{
			"output_index": outputIndex,
			"item":         withStatus(item, "in_progress"),
		})

		switch item["type"] {
		case "message":
			content, _ := item["content"].([]interface{})
			for ci, cRaw := range content {
				cm, ok := cRaw.(map[string]interface{})
				if !ok {
					continue
				}
				text, _ := cm["text"].(string)
				if text != "" {
					writeEvent("response.output_text.delta", map[string]interface{}{
						"item_id":       itemID,
						"output_index":  outputIndex,
						"content_index": ci,
						"delta":         text,
					})
					writeEvent("response.output_text.done", map[string]interface{}{
						"item_id":       itemID,
						"output_index":  outputIndex,
						"content_index": ci,
						"text":          text,
					})
				}
			}
		case "function_call":
			args, _ := item["arguments"].(string)
			writeEvent("response.function_call_arguments.delta", map[string]interface{}{
				"item_id":      itemID,
				"output_index": outputIndex,
				"delta":        args,
			})
			writeEvent("response.function_call_arguments.done", map[string]interface{}{
				"item_id":      itemID,
				"output_index": outputIndex,
				"arguments":    args,
			})
		}

		writeEvent("response.output_item.done", map[string]interface{}{
			"output_index": outputIndex,
			"item":         withStatus(item, "completed"),
		})
	}

	writeEvent("response.completed", map[string]interface{}{"response": resp})
	return nil
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func withStatus(item map[string]interface{}, status string) map[string]interface{} {
	out := cloneMap(item)
	out["status"] = status
	return out
}
