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

func anthropicToOpenAIStream(reader io.Reader) io.Reader {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		scanner := bufio.NewScanner(reader)
		var id string
		var model string
		roleSent := false

		for scanner.Scan() {
			line := scanner.Text()

			if strings.TrimSpace(line) == "" {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
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

			case "content_block_delta":
				delta, _ := event["delta"].(map[string]interface{})
				if delta == nil {
					continue
				}
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
