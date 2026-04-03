package llm

import (
	"ai-gateway/config"
	"context"
	"fmt"

	"github.com/go-resty/resty/v2"
)

type Usage struct{
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Provider 定义模型供应商接口
type Provider interface{
	Name() string
	Invoke(ctx context.Context, prompt string) (string, Usage, error) // 🌟 增加 Usage 返回
	
}

// BaseClient 基础客户端（OpenAI 协议兼容）
type BaseClient struct{
	name string
	client *resty.Client
	apiKey string
	apiURL string
	model  string
}

func NewBaseClient(name,url,key,model string)*BaseClient{
	return &BaseClient{
		name:   name,
		client: resty.New(),
		apiURL: url,
		apiKey: key,
		model:  model,
	}
}

func (c *BaseClient) Name() string { return c.name }

func (c *BaseClient) Invoke(ctx context.Context, prompt string) (string, Usage, error) {
	var result Response

	resp, err := c.client.R().
		SetContext(ctx).
		SetAuthToken(c.apiKey).
		SetHeader("Content-Type", "application/json").
		SetBody(Request{
			Model: c.model,
			Messages: []Message{
				{Role: "user", Content: prompt},
			},
		}).
		SetResult(&result). // Resty 会自动把 JSON 解析到 result.Usage 里
		Post(c.apiURL)

	if err != nil {
		// 🌟 报错时也要返回空的 Usage
		return "", Usage{}, fmt.Errorf("network error: %v", err)
	}

	if resp.IsError() {
		return "", Usage{}, fmt.Errorf("API返回错误 | 状态码: %d | 内容: %s", resp.StatusCode(), resp.String())
	}

	if len(result.Choices) > 0 {
		// 🌟 成功：返回内容和解析出来的 Usage
		return result.Choices[0].Message.Content, result.Usage, nil
	}

	return "", Usage{}, fmt.Errorf("no response from AI")
}

// Request 和 Response 结构体保持不变...
type Request struct {
	Model    string    `json:"model"`
	Messages[]Message `json:"messages"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Choices[]struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"` // 🌟 新增：捕获 Token 使用情况
}

// ================= 核心重构部分 =================

// LLMClient 定义大模型客户端结构体
type LLMClient struct {
	restyClient *resty.Client // 复用底层的 HTTP 客户端（自带连接池）
	apiKey      string
	apiURL      string
	model       string
}

// NewLLMClient 是 LLMClient 的构造函数（工厂模式）
// 导师注：在 main 函数初始化时，只调用一次这个函数，实现全局复用！
func NewLLMClient() *LLMClient {
	return &LLMClient{
		restyClient: resty.New(), // 全局只 New 一次！
		apiKey:      config.GetEnv("LLM_API_KEY", ""),
		apiURL:      config.GetEnv("LLM_API_URL", ""),
		model:       config.GetEnv("LLM_MODEL", ""), // 解决硬编码，从配置读取
	}
}

// InvokeLLM 变成了 LLMClient 的方法 (Receiver Method)
func (c *LLMClient) InvokeLLM(ctx context.Context, prompt string) (string, error) {
	var result Response

	// 发起 HTTP 请求，使用结构体内部缓存的配置和复用的 client
	resp, err := c.restyClient.R().
		SetContext(ctx).
		SetAuthToken(c.apiKey).
		SetHeader("Content-Type", "application/json").
		SetBody(Request{
			Model: c.model, // 使用配置中的模型名称
			Messages:[]Message{
				{Role: "user", Content: prompt},
			},
		}).
		SetResult(&result).
		Post(c.apiURL)

	if err != nil {
		return "", fmt.Errorf("network error: %v", err)
	}

	if resp.IsError() {
		return "", fmt.Errorf("API返回错误 | 状态码: %d | 内容: %s", resp.StatusCode(), resp.String())
		// return "", fmt.Errorf("api error: %s", resp.String())
	}

	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("no response from AI")
}