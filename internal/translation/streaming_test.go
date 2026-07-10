package translation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSSETranslator_Passthrough(t *testing.T) {
	// Same format should pass through
	input := strings.NewReader("data: hello\n\n")
	out := SSETranslator(input, "openai", "openai")
	if out != input {
		t.Fatal("expected same reader for passthrough")
	}
}

func TestSSETranslator_OpenAIToAnthropic(t *testing.T) {
	// openai -> anthropic is now implemented
	input := strings.NewReader("data: hello\n\n")
	out := SSETranslator(input, "openai", "anthropic")
	if out == input {
		t.Fatal("expected a new reader for openai->anthropic")
	}
}

func TestAnthropicToOpenAIStream_Text(t *testing.T) {
	input := `data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3"}}

data: {"type":"content_block_delta","index":0,"delta":{"text":"Hello"}}

data: {"type":"content_block_delta","index":0,"delta":{"text":" world"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":2}}

data: {"type":"message_stop"}

`

	out := SSETranslator(strings.NewReader(input), "anthropic", "openai")
	lines := readAllLines(t, out)

	// Should have multiple data lines + [DONE]
	if len(lines) == 0 {
		t.Fatal("expected output lines")
	}

	// Check first chunk has id, model, role
	first := parseDataLine(t, lines[0])
	if first["id"] != "msg_01" {
		t.Errorf("expected id=msg_01, got %v", first["id"])
	}
	if first["object"] != "chat.completion.chunk" {
		t.Errorf("expected object=chat.completion.chunk, got %v", first["object"])
	}

	// Check text content
	var foundContent []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		choices, _ := event["choices"].([]interface{})
		if len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			delta, _ := choice["delta"].(map[string]interface{})
			if content, ok := delta["content"].(string); ok {
				foundContent = append(foundContent, content)
			}
		}
	}
	if len(foundContent) != 2 {
		t.Fatalf("expected 2 content chunks, got %d: %v", len(foundContent), foundContent)
	}
	if foundContent[0] != "Hello" {
		t.Errorf("expected first content=Hello, got %q", foundContent[0])
	}
	if foundContent[1] != " world" {
		t.Errorf("expected second content=' world', got %q", foundContent[1])
	}

	// Check [DONE] somewhere in output
	foundDone := false
	for _, line := range lines {
		if line == "data: [DONE]" {
			foundDone = true
			break
		}
	}
	if !foundDone {
		t.Fatalf("expected [DONE] in output, got lines: %v", lines)
	}
}

func TestAnthropicToOpenAIStream_Thinking(t *testing.T) {
	input := `data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3"}}

data: {"type":"thinking_delta","delta":{"thinking":"Let me think..."}}

data: {"type":"message_stop"}

`

	out := SSETranslator(strings.NewReader(input), "anthropic", "openai")
	lines := readAllLines(t, out)

	var foundThinking string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		choices, _ := event["choices"].([]interface{})
		if len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			delta, _ := choice["delta"].(map[string]interface{})
			if reasoning, ok := delta["reasoning"].(string); ok {
				foundThinking = reasoning
			}
		}
	}
	if foundThinking != "Let me think..." {
		t.Errorf("expected thinking='Let me think...', got %q", foundThinking)
	}
}

func TestAnthropicToOpenAIStream_ToolUse(t *testing.T) {
	input := `data: {"type":"message_start","message":{"id":"msg_01","model":"claude-3"}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_123","name":"get_weather"}}

data: {"type":"content_block_delta","index":0,"delta":{"partial_json":"{\"loc"}}

data: {"type":"content_block_delta","index":0,"delta":{"partial_json":"ation\":\"NYC\"}"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

data: {"type":"message_stop"}

`

	out := SSETranslator(strings.NewReader(input), "anthropic", "openai")
	lines := readAllLines(t, out)

	var foundToolCalls []map[string]interface{}
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		choices, _ := event["choices"].([]interface{})
		if len(choices) > 0 {
			choice := choices[0].(map[string]interface{})
			delta, _ := choice["delta"].(map[string]interface{})
			if tcs, ok := delta["tool_calls"].([]interface{}); ok {
				for _, tc := range tcs {
					foundToolCalls = append(foundToolCalls, tc.(map[string]interface{}))
				}
			}
		}
	}

	if len(foundToolCalls) < 3 {
		t.Fatalf("expected at least 3 tool_call chunks, got %d", len(foundToolCalls))
	}

	// First chunk should have id, name, type
	first := foundToolCalls[0]
	if first["id"] != "tool_123" {
		t.Errorf("expected tool id=tool_123, got %v", first["id"])
	}
	if first["type"] != "function" {
		t.Errorf("expected type=function, got %v", first["type"])
	}
	fn, _ := first["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", fn["name"])
	}

	// Second and third should have arguments
	second := foundToolCalls[1]
	fn2, _ := second["function"].(map[string]interface{})
	if fn2["arguments"] != "{\"loc" {
		t.Errorf("expected first arg fragment, got %q", fn2["arguments"])
	}
}

func TestOpenAIToAnthropicStream_Text(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundTypes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
		}
	}

	if len(foundTypes) == 0 {
		t.Fatal("expected event types")
	}

	// Should have message_start, content_block_delta (text), message_delta, message_stop
	if foundTypes[0] != "message_start" {
		t.Errorf("expected first event=message_start, got %q", foundTypes[0])
	}
	if foundTypes[len(foundTypes)-1] != "message_stop" {
		t.Errorf("expected last event=message_stop, got %q", foundTypes[len(foundTypes)-1])
	}
}

func TestOpenAIToAnthropicStream_Reasoning(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"Let me think..."}}]}

data: [DONE]

`

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	// Collect (type, index) for every block event so we can assert there are no
	// index collisions and that start/stop indices line up.
	type blockEvent struct {
		typ   string
		index float64
	}
	var blockEvents []blockEvent
	var foundThinking string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		switch event["type"] {
		case "content_block_start", "content_block_stop":
			idx, _ := event["index"].(float64)
			blockEvents = append(blockEvents, blockEvent{event["type"].(string), idx})
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]interface{})
			if thinking, ok := delta["thinking"].(string); ok {
				foundThinking = thinking
			}
		}
	}
	if foundThinking != "Let me think..." {
		t.Errorf("expected thinking='Let me think...', got %q", foundThinking)
	}

	// No two content_block_start events may share the same index (protocol
	// violation that makes strict clients disconnect).
	started := map[float64]bool{}
	for _, e := range blockEvents {
		if e.typ == "content_block_start" {
			if started[e.index] {
				t.Errorf("duplicate content_block_start at index %v", e.index)
			}
			started[e.index] = true
		}
	}
}

func TestOpenAIToAnthropicStream_ToolUse(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NYC\"}"}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundTypes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
		}
	}

	// Should have message_start, content_block_start, content_block_delta, content_block_delta, message_delta, message_stop
	if len(foundTypes) < 4 {
		t.Fatalf("expected at least 4 events, got %d: %v", len(foundTypes), foundTypes)
	}

	if foundTypes[1] != "content_block_start" {
		t.Errorf("expected content_block_start, got %q", foundTypes[1])
	}
	if foundTypes[2] != "content_block_delta" {
		t.Errorf("expected content_block_delta, got %q", foundTypes[2])
	}
}

func TestOpenAIToAnthropicStream_UsageOnly(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[]}

data: [DONE]

`

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundUsage bool
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		if event["type"] == "message_delta" {
			if _, ok := event["usage"]; ok {
				foundUsage = true
			}
		}
	}
	if !foundUsage {
		t.Fatal("expected usage in message_delta")
	}
}

func TestAnthropicToOpenAIStream_LargeLine(t *testing.T) {
	// Build an SSE event with a data line larger than the old 64KB scanner limit
	largeText := strings.Repeat("A", 100*1024) // 100KB of text
	event := fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"text":"%s"}}`, largeText)

	input := event + "\n\ndata: {\"type\":\"message_stop\"}\n\n"

	out := SSETranslator(strings.NewReader(input), "anthropic", "openai")
	lines := readAllLines(t, out)

	// Should not error and should produce output lines
	if len(lines) == 0 {
		t.Fatal("expected output lines for large SSE event")
	}
}

func TestOpenAIToAnthropicStream_LargeLine(t *testing.T) {
	largeText := strings.Repeat("B", 100*1024) // 100KB of text
	event := fmt.Sprintf(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"%s"}}]}`, largeText)

	input := event + "\n\ndata: [DONE]\n\n"

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	if len(lines) == 0 {
		t.Fatal("expected output lines for large SSE event")
	}
}

func TestIsStreamingResponse(t *testing.T) {
	header := http.Header{}
	header.Set("Content-Type", "text/event-stream")
	if !IsStreamingResponse(header) {
		t.Fatal("expected true for text/event-stream")
	}

	header.Set("Content-Type", "application/json")
	if IsStreamingResponse(header) {
		t.Fatal("expected false for application/json")
	}

	header.Del("Content-Type")
	if IsStreamingResponse(header) {
		t.Fatal("expected false for missing Content-Type")
	}
}

func TestOpenAIToAnthropicStream_MessageStartWithoutRole(t *testing.T) {
	// First chunk has content but no role — message_start should still be emitted
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundTypes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(foundTypes) < 4 {
		t.Fatalf("expected at least 4 events, got %d: %v", len(foundTypes), foundTypes)
	}
	if foundTypes[0] != "message_start" {
		t.Errorf("expected first event=message_start, got %q", foundTypes[0])
	}
	if foundTypes[1] != "content_block_start" {
		t.Errorf("expected content_block_start, got %q", foundTypes[1])
	}
	if foundTypes[2] != "content_block_delta" {
		t.Errorf("expected content_block_delta, got %q", foundTypes[2])
	}
	if foundTypes[len(foundTypes)-1] != "message_stop" {
		t.Errorf("expected last event=message_stop, got %q", foundTypes[len(foundTypes)-1])
	}
}

func TestOpenAIToAnthropicStream_ToolCallContentBlockStop(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NYC\"}"}}]}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundTypes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
		}
	}

	// Should have: message_start, content_block_start, content_block_delta, content_block_delta, content_block_stop, message_delta, message_stop
	if len(foundTypes) < 7 {
		t.Fatalf("expected at least 7 events, got %d: %v", len(foundTypes), foundTypes)
	}

	// Check content_block_stop appears before message_delta
	stopIdx := -1
	deltaIdx := -1
	for i, et := range foundTypes {
		if et == "content_block_stop" {
			stopIdx = i
		}
		if et == "message_delta" {
			deltaIdx = i
		}
	}
	if stopIdx == -1 {
		t.Fatal("expected content_block_stop in output")
	}
	if deltaIdx == -1 {
		t.Fatal("expected message_delta in output")
	}
	if stopIdx >= deltaIdx {
		t.Fatalf("expected content_block_stop before message_delta, got stopIdx=%d deltaIdx=%d", stopIdx, deltaIdx)
	}
}

func TestOpenAIToAnthropicStream_TextContentBlockStart(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":" world"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundTypes []string
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			foundTypes = append(foundTypes, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(foundTypes) < 5 {
		t.Fatalf("expected at least 5 events, got %d: %v", len(foundTypes), foundTypes)
	}
	if foundTypes[1] != "content_block_start" {
		t.Errorf("expected content_block_start as second event, got %q", foundTypes[1])
	}
}

func TestOpenAIToAnthropicStream_FunctionCallFinishReason(t *testing.T) {
	input := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"gpt-4","choices":[{"index":0,"delta":{"content":""},"finish_reason":"function_call"}]}

data: [DONE]

`
	out := OpenAIToAnthropicStream(strings.NewReader(input))
	lines := readAllLines(t, out)

	var foundStopReason string
	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		event := parseDataLine(t, line)
		if event["type"] == "message_delta" {
			delta, _ := event["delta"].(map[string]interface{})
			if sr, ok := delta["stop_reason"].(string); ok {
				foundStopReason = sr
			}
		}
	}
	if foundStopReason != "tool_use" {
		t.Errorf("expected stop_reason=tool_use for function_call finish, got %q", foundStopReason)
	}
}

// Helpers

func readAllLines(t *testing.T, r io.Reader) []string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	const maxCapacity = 512 * 1024
	scanBuf := make([]byte, 4096)
	scanner.Buffer(scanBuf, maxCapacity)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read error: %v", err)
	}
	return lines
}

func parseDataLine(t *testing.T, line string) map[string]interface{} {
	t.Helper()
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected data: prefix, got %q", line)
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return map[string]interface{}{"done": true}
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	return result
}
