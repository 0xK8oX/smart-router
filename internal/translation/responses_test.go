package translation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// toMaps converts a []interface{} of map elements (as produced by the
// converters) back to []map[string]interface{} for easy assertions.
func toMaps(t *testing.T, v interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := v.([]interface{})
	if !ok {
		t.Fatalf("expected []interface{}, got %T", v)
	}
	out := make([]map[string]interface{}, len(raw))
	for i, m := range raw {
		mm, ok := m.(map[string]interface{})
		if !ok {
			t.Fatalf("element %d not a map: %T", i, m)
		}
		out[i] = mm
	}
	return out
}

func TestResponsesRequestToChat_Basic(t *testing.T) {
	body := map[string]interface{}{
		"model":        "auto-volcengine",
		"instructions": "Be concise.",
		"input":        "Hello",
	}

	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["model"] != "auto-volcengine" {
		t.Errorf("model mismatch: got %v", out["model"])
	}

	msgs := toMaps(t, out["messages"])
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %v", out["messages"])
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "Be concise." {
		t.Errorf("system message wrong: %v", msgs[0])
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "Hello" {
		t.Errorf("user message wrong: %v", msgs[1])
	}
}

func TestResponsesRequestToChat_MergesSystemMessages(t *testing.T) {
	// Codex sends instructions AND a developer-role item in input. Both must
	// collapse into a single leading system message (templates like
	// Claude-Mythos reject a non-first system message).
	body := map[string]interface{}{
		"model":        "m",
		"instructions": "You are helpful.",
		"input": []interface{}{
			map[string]interface{}{"type": "message", "role": "developer", "content": "Extra rules."},
			map[string]interface{}{"type": "message", "role": "user", "content": "hi"},
		},
	}
	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msgs := toMaps(t, out["messages"])
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (merged system + user), got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "system" {
		t.Errorf("expected first msg system, got %v", msgs[0]["role"])
	}
	sys, _ := msgs[0]["content"].(string)
	if !strings.Contains(sys, "You are helpful.") || !strings.Contains(sys, "Extra rules.") {
		t.Errorf("expected merged system content, got %q", sys)
	}
	if msgs[1]["role"] != "user" {
		t.Errorf("expected second msg user, got %v", msgs[1]["role"])
	}
	// No second system message anywhere.
	for i, m := range msgs {
		if i > 0 && m["role"] == "system" {
			t.Errorf("unexpected non-leading system message at index %d", i)
		}
	}
}

func TestResponsesRequestToChat_ContentArray(t *testing.T) {
	body := map[string]interface{}{
		"model": "m",
		"input": []interface{}{
			map[string]interface{}{
				"type": "message", "role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "hi"},
					map[string]interface{}{"type": "output_text", "text": "there"},
				},
			},
		},
	}

	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msgs := toMaps(t, out["messages"])
	if msgs[0]["content"] != "hi\nthere" {
		t.Errorf("expected joined text, got %v", msgs[0]["content"])
	}
}

func TestResponsesRequestToChat_ToolCallRoundTrip(t *testing.T) {
	body := map[string]interface{}{
		"model": "m",
		"input": []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": "weather?"},
			map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"NYC"}`},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": `{"temp":72}`},
		},
	}

	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msgs := toMaps(t, out["messages"])
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0]["role"] != "user" {
		t.Errorf("msg0 role: got %v", msgs[0]["role"])
	}
	// Consecutive function_call items coalesce into one assistant message.
	if msgs[1]["role"] != "assistant" {
		t.Errorf("msg1 role: got %v", msgs[1]["role"])
	}
	tcs := toMaps(t, msgs[1]["tool_calls"])
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %v", msgs[1]["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]interface{})
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"NYC"}` {
		t.Errorf("tool_call wrong: %v", tcs[0])
	}
	if tcs[0]["id"] != "call_1" {
		t.Errorf("tool_call id: got %v", tcs[0]["id"])
	}
	// function_call_output -> tool message with matching tool_call_id.
	if msgs[2]["role"] != "tool" {
		t.Errorf("msg2 role: got %v", msgs[2]["role"])
	}
	if msgs[2]["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id: got %v", msgs[2]["tool_call_id"])
	}
}

func TestResponsesRequestToChat_Tools(t *testing.T) {
	body := map[string]interface{}{
		"model": "m",
		"input": "hi",
		"tools": []interface{}{
			map[string]interface{}{
				"type":        "function",
				"name":        "get_weather",
				"description": "Get weather",
				"parameters":  map[string]interface{}{"type": "object"},
			},
		},
		"tool_choice": map[string]interface{}{"type": "function", "name": "get_weather"},
	}

	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tools := toMaps(t, out["tools"])
	if tools[0]["type"] != "function" {
		t.Errorf("expected type=function, got %v", tools[0]["type"])
	}
	fn := tools[0]["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("expected name=get_weather, got %v", fn["name"])
	}
	tc := out["tool_choice"].(map[string]interface{})
	if tc["type"] != "function" {
		t.Errorf("tool_choice type: got %v", tc["type"])
	}
}

func TestResponsesRequestToChat_ToolChoiceStrings(t *testing.T) {
	cases := []struct {
		in   interface{}
		want interface{}
	}{
		{map[string]interface{}{"type": "auto"}, "auto"},
		{map[string]interface{}{"type": "none"}, "none"},
		{map[string]interface{}{"type": "required"}, "required"},
		{map[string]interface{}{"type": "any"}, "required"},
		{map[string]interface{}{"type": "function", "name": "get_weather"},
			map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "get_weather"}}},
		{"auto", "auto"},
	}
	for _, c := range cases {
		got := responsesToolChoiceToChat(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tool_choice %v: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestResponsesRequestToChat_DropsStatelessFields(t *testing.T) {
	body := map[string]interface{}{
		"model":                "m",
		"input":                "hi",
		"store":                true,
		"previous_response_id": "resp_abc",
		"reasoning":            map[string]interface{}{"effort": "high"},
	}
	out, err := ResponsesRequestToChat(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, k := range []string{"store", "previous_response_id", "reasoning"} {
		if _, ok := out[k]; ok {
			t.Errorf("expected %q dropped, but present", k)
		}
	}
	if _, ok := out["max_tokens"]; ok {
		// max_output_tokens maps to max_tokens; absent here is correct.
		t.Errorf("max_tokens should be absent when max_output_tokens not set")
	}
}

func TestResponsesRequestToChat_MaxOutputTokens(t *testing.T) {
	out, _ := ResponsesRequestToChat(map[string]interface{}{
		"model": "m", "input": "hi", "max_output_tokens": 1234,
	})
	if out["max_tokens"] != 1234 {
		t.Errorf("max_tokens: got %v", out["max_tokens"])
	}
}

func TestChatCompletionToResponses_TextOnly(t *testing.T) {
	data := []byte(`{
		"id":"chatcmpl-1","object":"chat.completion","model":"glm-5.2",
		"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Hi there!"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
	}`)
	out, err := ChatCompletionToResponses(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var r map[string]interface{}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r["object"] != "response" {
		t.Errorf("object: got %v", r["object"])
	}
	if !strings.HasPrefix(r["id"].(string), "resp_") {
		t.Errorf("id prefix: got %v", r["id"])
	}
	if r["status"] != "completed" {
		t.Errorf("status: got %v", r["status"])
	}
	output := r["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(output))
	}
	msg := output[0].(map[string]interface{})
	if msg["type"] != "message" {
		t.Errorf("type: got %v", msg["type"])
	}
	content := msg["content"].([]interface{})
	text := content[0].(map[string]interface{})
	if text["type"] != "output_text" || text["text"] != "Hi there!" {
		t.Errorf("output_text wrong: %v", text)
	}
	usage := r["usage"].(map[string]interface{})
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) {
		t.Errorf("usage wrong: %v", usage)
	}
}

func TestChatCompletionToResponses_ToolCallsOnly(t *testing.T) {
	data := []byte(`{
		"id":"chatcmpl-2","object":"chat.completion","model":"glm-5.2",
		"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_42","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}
		]}}],
		"usage":{"prompt_tokens":3,"completion_tokens":1}
	}`)
	out, err := ChatCompletionToResponses(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var r map[string]interface{}
	json.Unmarshal(out, &r)
	output := r["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("expected only a function_call item, got %d items", len(output))
	}
	fc := output[0].(map[string]interface{})
	if fc["type"] != "function_call" {
		t.Errorf("type: got %v", fc["type"])
	}
	// id and call_id both reuse the upstream tool_call id (round-trip invariant).
	if fc["id"] != "call_42" || fc["call_id"] != "call_42" {
		t.Errorf("id/call_id: got %v / %v", fc["id"], fc["call_id"])
	}
	if fc["name"] != "shell" {
		t.Errorf("name: got %v", fc["name"])
	}
	if fc["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("arguments: got %v", fc["arguments"])
	}
}

func TestChatCompletionToResponses_FinishReasonLength(t *testing.T) {
	data := []byte(`{
		"id":"chatcmpl-3","model":"m",
		"choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"partial"}}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`)
	out, _ := ChatCompletionToResponses(data)
	var r map[string]interface{}
	json.Unmarshal(out, &r)
	if r["status"] != "incomplete" {
		t.Errorf("expected status=incomplete, got %v", r["status"])
	}
}
