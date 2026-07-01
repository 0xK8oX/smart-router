package translation

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	return string(b)
}

func TestChatSSEToResponses_TextStream(t *testing.T) {
	// A realistic openai chat-completion SSE stream: role, then a few text
	// deltas, then finish_reason, then [DONE].
	upstream := "" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm","choices":[{"index":0,"delta":{"content":"Hello"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm","choices":[{"index":0,"delta":{"content":", world"}}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"glm","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\n" +
		`data: [DONE]` + "\n\n"

	rr := httptest.NewRecorder()
	_ = rr // translator writes to its returned io.Reader; headers are set by the handler in prod

	body := readAll(t, ChatSSEToResponses(strings.NewReader(upstream)))
	// The translator should produce the full Responses event sequence in order,
	// ending with response.completed carrying the assembled output + usage.
	for _, want := range []string{
		"event: response.created",
		`"type":"response.created"`,
		"event: response.output_item.added",
		`"delta":"Hello"`,
		`"delta":", world"`,
		"event: response.output_text.done",
		`"text":"Hello, world"`,
		"event: response.output_item.done",
		"event: response.completed",
		`"type":"response.completed"`,
		`"input_tokens":7`,
		`"output_tokens":3`,
		`"status":"completed"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in stream:\n%s", want, body)
		}
	}
	// Ordering: created < first delta < output_text.done < output_item.done < completed.
	created := strings.Index(body, "response.created")
	firstDelta := strings.Index(body, "response.output_text.delta")
	doneText := strings.Index(body, "response.output_text.done")
	doneItem := strings.Index(body, "response.output_item.done")
	completed := strings.LastIndex(body, "response.completed")
	if !(created < firstDelta && firstDelta < doneText && doneText < doneItem && doneItem < completed) {
		t.Errorf("event order wrong: created=%d delta=%d doneText=%d doneItem=%d completed=%d",
			created, firstDelta, doneText, doneItem, completed)
	}
}

func TestWriteResponsesSSE_Message(t *testing.T) {
	responsesBytes := []byte(`{
		"id":"resp_1","object":"response","status":"completed","model":"m",
		"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello"}]}],
		"usage":{"input_tokens":1,"output_tokens":2}
	}`)

	rr := httptest.NewRecorder()
	if err := WriteResponsesSSE(rr, responsesBytes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type: got %q", got)
	}
	if rr.Code != 200 {
		t.Errorf("status: got %d", rr.Code)
	}

	body := rr.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		`"delta":"hello"`,
		"event: response.output_text.done",
		`"text":"hello"`,
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in stream:\n%s", want, body)
		}
	}

	// Ordering: created < delta < output_item.done < completed.
	created := strings.Index(body, "response.created")
	delta := strings.Index(body, "response.output_text.delta")
	done := strings.Index(body, "response.output_item.done")
	completed := strings.LastIndex(body, "response.completed")
	if !(created < delta && delta < done && done < completed) {
		t.Errorf("event order wrong: created=%d delta=%d done=%d completed=%d", created, delta, done, completed)
	}
	// created carries the in_progress skeleton; completed carries usage.
	if !strings.Contains(body, `"status":"in_progress"`) {
		t.Errorf("expected in_progress skeleton in response.created")
	}
	if !strings.Contains(body, `"input_tokens":1`) {
		t.Errorf("expected usage in response.completed")
	}
}

func TestWriteResponsesSSE_FunctionCall(t *testing.T) {
	responsesBytes := []byte(`{
		"id":"resp_2","object":"response","status":"completed","model":"m",
		"output":[{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"shell","arguments":"{\"cmd\":\"ls\"}","status":"completed"}]
	}`)

	rr := httptest.NewRecorder()
	if err := WriteResponsesSSE(rr, responsesBytes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"event: response.output_item.added",
		"event: response.function_call_arguments.delta",
		`"delta":"{\"cmd\":\"ls\"}"`,
		"event: response.function_call_arguments.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in stream:\n%s", want, body)
		}
	}
}

func TestWriteResponsesSSE_MultipleItemsIndexed(t *testing.T) {
	// Two output items must each carry the correct output_index.
	responsesBytes := []byte(`{
		"id":"resp_3","object":"response","status":"completed","model":"m",
		"output":[
			{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"a"}]},
			{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"x","arguments":"{}","status":"completed"}
		]
	}`)
	rr := httptest.NewRecorder()
	if err := WriteResponsesSSE(rr, responsesBytes); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := rr.Body.String()
	if c := strings.Count(body, "event: response.output_item.added"); c != 2 {
		t.Errorf("expected 2 output_item.added events, got %d", c)
	}
	if c := strings.Count(body, "event: response.output_item.done"); c != 2 {
		t.Errorf("expected 2 output_item.done events, got %d", c)
	}
}
