package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"ai-gateway/pkg/metrics" 
	"time"

	"github.com/sony/gobreaker"
)

type LLMRouter struct {
	providers []Provider
	breakers  map[string]*gobreaker.CircuitBreaker
}

func NewLLMRouter(providers ...Provider) *LLMRouter {
	breakers := make(map[string]*gobreaker.CircuitBreaker)
	
	for _, p := range providers {
		// 为每个供应商配置熔断器
		st := gobreaker.Settings{
			Name:        p.Name(),
			MaxRequests: 3,               // 半开状态允许通过的请求数
			Interval:    5 * time.Second, // 定期清空计数器
			Timeout:     30 * time.Second, // 熔断器开启后，多久进入半开状态
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				// 熔断条件：连续失败超过 5 次，或者失败率超过 50%
				return counts.ConsecutiveFailures > 5
			},
			OnStateChange: func(name string, from, to gobreaker.State) {
				log.Printf("🚨 熔断器 [%s] 状态变更: %v -> %v", name, from, to)
			},
		}
		breakers[p.Name()] = gobreaker.NewCircuitBreaker(st)
	}

	return &LLMRouter{
		providers: providers,
		breakers:  breakers,
	}
}

// InvokeWithFallback 核心逻辑：按顺序尝试，如果熔断或报错则跳到下一个
// InvokeWithFallback 核心逻辑：增加 Usage 返回值
// pkg/llm/router.go

// pkg/llm/router.go

func (r *LLMRouter) InvokeWithFallback(ctx context.Context, prompt string) (string, Usage, error) {
	var lastErr error

	for _, p := range r.providers {
		cb := r.breakers[p.Name()]

		// 🌟 1. 定义一个“书包”结构体，用来装我们要带出来的两个宝贝
		type resultWrapper struct {
			content string
			usage   Usage
		}

		// 内部重试 3 次
		for attempt := 1; attempt <= 3; attempt++ {
			start := time.Now()

			// 🌟 2. 这里的匿名函数必须只返回 (interface{}, error)
			rawRes, err := cb.Execute(func() (interface{}, error) {
				content, usage, err := p.Invoke(ctx, prompt) // 这里返回 3 个值
				if err != nil {
					return nil, err // 失败时返回 nil 和 error
				}
				// 🌟 关键：把 content 和 usage 塞进书包，作为一个整体 (interface{}) 返回
				return resultWrapper{content, usage}, nil
			})

			// 记录监控指标
			metrics.LLMLatency.WithLabelValues(p.Name()).Observe(time.Since(start).Seconds())

			if err == nil {
				// 🌟 3. 成功了！把书包打开，取出里面的东西
				data := rawRes.(resultWrapper) 
				return data.content, data.usage, nil
			}

			// --- 失败处理逻辑 ---
			lastErr = err
			
			// 如果熔断器开了，直接换供应商，别试了
			if errors.Is(err, gobreaker.ErrOpenState) {
				log.Printf("🚨 [%s] 熔断器已开启，跳过重试", p.Name())
				break 
			}

			log.Printf("⚠️ [%s] 第 %d 次尝试失败: %v", p.Name(), attempt, err)
			if attempt < 3 {
				time.Sleep(time.Second * time.Duration(attempt))
			}
		}
	}

	return "", Usage{}, fmt.Errorf("全线崩溃，最后错误: %w", lastErr)
}