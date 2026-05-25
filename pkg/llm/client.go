package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Provider 定义模型供应商接口
type Provider interface {
	Name() string
	Models()[]string 
	Invoke(ctx context.Context, model string, prompt string) (string, Usage, error)
	InvokeStream(ctx context.Context, model string, prompt string) (<-chan StreamMessage, error)
}

type BaseClient struct {
	name       string
	client     *resty.Client
	httpClient *http.Client
	apiKey     string
	apiURL     string
	models[]string // 🆕 改为切片存储
}

type StreamMessage struct {
	Content string
	Usage   *Usage
}

func NewBaseClient(name, url, key, modelsStr string) *BaseClient {
	restyCli := resty.New()
	restyCli.SetTimeout(300 * time.Second)
	
	// 将数据库里逗号分隔的字符串解析为切片
	var models[]string
	for _, m := range strings.Split(modelsStr, ",") {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			models = append(models, trimmed)
		}
	}

	return &BaseClient{
		name:   name,
		client: restyCli,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		apiURL: url,
		apiKey: key,
		models: models,
	}
}

func (c *BaseClient) Name() string { return c.name }
func (c *BaseClient) Models()[]string { return c.models } // 🆕 实现接口方法

func (c *BaseClient) Invoke(ctx context.Context, model string, prompt string) (string, Usage, error){
	var result Response

	resp, err := c.client.R().
		SetContext(ctx).
		SetAuthToken(c.apiKey).
		SetHeader("Content-Type", "application/json").
		SetBody(Request{
			Model: model,
			Messages: []Message{
				{Role: "user", Content: prompt},
			},
		}).
		SetResult(&result). // Resty 会自动把 JSON 解析到 result.Usage 里
		Post(c.apiURL)

	if err != nil {
		//报错时也要返回空的 Usage
		return "", Usage{}, fmt.Errorf("network error: %v", err)
	}

	if resp.IsError() {
		return "", Usage{}, fmt.Errorf("API返回错误 | 状态码: %d | 内容: %s", resp.StatusCode(), resp.String())
	}

	if len(result.Choices) > 0 {
		//成功：返回内容和解析出来的 Usage
		return result.Choices[0].Message.Content, result.Usage, nil
	}

	return "", Usage{}, fmt.Errorf("no response from AI")
}

//流式调用实现
func (c *BaseClient) InvokeStream(ctx context.Context, model string, prompt string) (<-chan StreamMessage, error) {
	reqBody := map[string]interface{}{
		"model": model, 
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": true,
		"stream_options": map[string]bool{"include_usage": true},
	}
	jsonData, _ := json.Marshal(reqBody)
	
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("network error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
	}

	ch := make(chan StreamMessage)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}

			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				break
			}

			var chunk struct {
				Choices[]struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *Usage `json:"usage"`
			}

			if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
				msg := StreamMessage{}
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
					msg.Content = chunk.Choices[0].Delta.Content
				}
				if chunk.Usage != nil {
					msg.Usage = chunk.Usage
				}

				if msg.Content != "" || msg.Usage != nil {
					// 🚨 致命 Bug 修复：增加 select 监听 ctx.Done()
					// 防止用户中途关掉网页导致 ch <- msg 永久阻塞，引发 Goroutine 泄漏
					select {
					case <-ctx.Done():
						return
					case ch <- msg:
					}
				}
			}
		}
	}()
	return ch, nil
}

type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"` //捕获 Token 使用情况
}
