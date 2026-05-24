package tokenizer

import (
	"encoding/json"

	"github.com/pkoukk/tiktoken-go"
)

var enc *tiktoken.Tiktoken

func init() {
	var err error
	enc, err = tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		panic("failed to load tiktoken encoding: " + err.Error())
	}
}

// CountString returns the tiktoken count for a single string.
func CountString(text string) int {
	if text == "" {
		return 0
	}
	return len(enc.Encode(text, nil, nil))
}

// CountMessages counts tokens in a messages array (OpenAI or Anthropic format).
// Adds a small per-message overhead to approximate role/special tokens.
func CountMessages(messages []map[string]interface{}) int {
	total := 0
	for _, m := range messages {
		if content, ok := m["content"].(string); ok {
			total += CountString(content)
		}
		// per-message overhead (role + formatting tokens)
		total += 4
	}
	return total
}

// CountOpenAIResponse counts output tokens from an OpenAI-format response body.
func CountOpenAIResponse(data []byte) int {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if len(data) == 0 {
		return 0
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Choices) == 0 {
		return 0
	}
	return CountString(resp.Choices[0].Message.Content)
}

// CountAnthropicResponse counts output tokens from an Anthropic-format response body.
func CountAnthropicResponse(data []byte) int {
	var resp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if len(data) == 0 {
		return 0
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Content) == 0 {
		return 0
	}
	return CountString(resp.Content[0].Text)
}
