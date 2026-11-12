package judge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type TokenUsage struct {
	PromptTokens      *int `json:"prompt_tokens"`
	CompletionTokens  *int `json:"completion_tokens"`
	CompletionDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// readChatStream only releases a complete stop+[DONE] reply to existing JSON consumers.
func readChatStream(body io.Reader) ([]byte, error) {
	const limit = 4 << 20
	r := &io.LimitedReader{R: body, N: limit + 1}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), limit+1)
	var event, content, reasoning strings.Builder
	eventType := ""
	finish := ""
	var usage json.RawMessage
	consume := func() (bool, error) {
		data := strings.TrimSpace(event.String())
		event.Reset()
		kind := eventType
		eventType = ""
		if kind == "error" {
			return false, fmt.Errorf("模型流式返回错误事件")
		}
		if data == "" {
			return false, nil
		}
		if data == "[DONE]" {
			if finish != "stop" {
				return false, fmt.Errorf("流式模型响应未正常完成")
			}
			return true, nil
		}
		var chunk struct {
			Usage   json.RawMessage `json:"usage"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index        int    `json:"index"`
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					Refusal   string `json:"refusal"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("无效的模型流式事件")
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return false, fmt.Errorf("模型流式返回错误事件")
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Delta.Refusal != "" {
				return false, fmt.Errorf("模型拒绝本次评审")
			}
			if choice.FinishReason != "" && choice.FinishReason != "stop" {
				return false, fmt.Errorf("模型流式输出未完成或已被截断")
			}
			if finish != "" && (choice.Delta.Content != "" || choice.Delta.Reasoning != "") {
				return false, fmt.Errorf("模型流式结束后仍返回正文")
			}
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.Reasoning)
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
		return false, nil
	}
	for sc.Scan() {
		if r.N <= 0 {
			return nil, fmt.Errorf("视觉模型响应超过 4 MiB 限制")
		}
		line := sc.Text()
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[6:])
		}
		if strings.HasPrefix(line, "data:") {
			event.WriteString(strings.TrimPrefix(line, "data:"))
			event.WriteByte('\n')
		}
		if line != "" {
			continue
		}
		done, err := consume()
		if err != nil {
			return nil, err
		}
		if done {
			return json.Marshal(map[string]any{"usage": usage, "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": content.String(), "reasoning_content": reasoning.String()}}}})
		}
	}
	if r.N <= 0 || sc.Err() == bufio.ErrTooLong {
		return nil, fmt.Errorf("视觉模型响应超过 4 MiB 限制")
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取模型流式响应失败: %w", err)
	}
	return nil, fmt.Errorf("模型流式响应中断，未收到完整结束标记")
}
