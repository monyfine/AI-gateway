package callback

import (
	"context"
	"fmt"
	"time"
	"github.com/go-resty/resty/v2"
)

type CallbackClient struct {
	httpCli *resty.Client
	baseURL string // 主系统的回调接口地址，比如 http://localhost:8080/api/callback
}

// CallbackRequest 发给主系统的 JSON 结构体
// 思考：主系统需要知道什么？肯定需要知道是哪个任务，以及 AI 生成的内容是什么。
type CallbackRequest struct {
	TaskID  string `json:"task_id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// NewCallbackClient 构造函数
func NewCallbackClient(baseURL string) *CallbackClient {
	client := resty.New()
	client.SetTimeout(10*time.Second)
	return &CallbackClient{
		httpCli: client,
		baseURL: baseURL,
	}
}

func (c *CallbackClient) SendResult(ctx context.Context, taskID string, aiResult string) error {
	//1. 组装请求体
	reqBody := CallbackRequest{
		TaskID:  taskID,
		Content: aiResult,
		Status:  "success",
	}
	// 2. 使用 resty 发送 POST 请求
	// 提示：resty 的链式调用非常优雅
	resp, err := c.httpCli.R().
		SetContext(ctx).  // 传入上下文，控制超时
		SetBody(reqBody). // resty 会自动把结构体转成 JSON
		Post(c.baseURL)   // 发送 POST 请求

	// 3. 错误处理
	if err != nil {
		return fmt.Errorf("请求主系统失败: %w", err)
	}

	// 4. 检查 HTTP 状态码 (主系统一般返回 200 才算成功)
	if resp.StatusCode() != 200 {
		return fmt.Errorf("主系统返回错误状态码: %d, 响应: %s", resp.StatusCode(), resp.String())
	}
	return nil
}
