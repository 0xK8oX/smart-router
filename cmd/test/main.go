package main

import (
	"fmt"
	"smart-router/internal/providers"
	"smart-router/internal/translation"
	"smart-router/internal/types"
)

func main() {
	body := map[string]interface{}{
		"model":    "gpt-4",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 5,
	}

	provider := types.ProviderConfig{
		Name:    "sam-volcengine-k2",
		BaseURL: "https://ark.cn-beijing.volces.com/api/coding",
		Model:   "kimi-k2.6",
		Format:  "anthropic",
		Timeout: 60,
		APIKey:  "06de8833-891c-4145-97a9-749bf03aac07",
	}

	translatedBody, err := translation.TranslateRequest(body, provider.Format)
	if err != nil {
		fmt.Printf("translate error: %v\n", err)
		return
	}
	fmt.Printf("translated body: %+v\n", translatedBody)

	client := providers.NewClient()
	resp, err := client.Call(provider, translatedBody)
	if err != nil {
		fmt.Printf("call error: %v\n", err)
		return
	}
	fmt.Printf("response status: %d\n", resp.StatusCode)
}
