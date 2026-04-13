package llm

import (
	"ai-gateway/pkg/metrics"
	"context"
	"errors"
	"fmt"
	"log"
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
			MaxRequests: 3,                // 半开状态允许通过的请求数
			Interval:    5 * time.Second,  // 定期清空计数器
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


func (r *LLMRouter) InvokeWithFallback(ctx context.Context, prompt string) (string, Usage, error) {
	var lastErr error

	for _, p := range r.providers {
		cb := r.breakers[p.Name()]

		// 🌟 1. 定义一个“书包”结构体，用来装我们要带出来的两个宝贝
		type resultWrapper struct {
			content string
			usage   Usage
			err     error // 用于装载“不计入熔断”的特殊错误
		}

		// 内部重试 3 次
		for attempt := 1; attempt <= 3; attempt++ {
			start := time.Now()

			// 🌟 2. 这里的匿名函数必须只返回 (interface{}, error)
			rawRes, err := cb.Execute(func() (interface{}, error) {
				content, usage, err := p.Invoke(ctx, prompt) // 这里返回 3 个值
				if err != nil {
					if errors.Is(err, context.Canceled){
						return resultWrapper{err: err}, nil
					}
					return nil, err // 失败时返回 nil 和 error
				}
				// 🌟 关键：把 content 和 usage 塞进书包，作为一个整体 (interface{}) 返回
				return resultWrapper{content: content, usage: usage}, nil
			})

			// 记录监控指标
			metrics.LLMLatency.WithLabelValues(p.Name()).Observe(time.Since(start).Seconds())

			if err == nil {
				// 🌟 3. 成功了！把书包打开，取出里面的东西
				data := rawRes.(resultWrapper)
				// 如果“书包”里装的是用户取消错误，直接返回，不再尝试其他供应商
				if data.err != nil {
					return "", Usage{}, data.err
				}

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

// 🌟 修复后的流式路由
func (r *LLMRouter) InvokeStreamWithFallback(ctx context.Context, prompt string) (<-chan StreamMessage, error) {
	var lastErr error

	for _, p := range r.providers {
		cb := r.breakers[p.Name()]

		rawRes, err := cb.Execute(func() (interface{}, error) {
			ch, err := p.InvokeStream(ctx, prompt)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil, err // 用户取消不计入熔断
				}
				// 🚨 致命 Bug 修复：这里原来漏了 return nil, err
				// 如果不 return，会导致向外返回 nil channel，引发永久死锁！
				return nil, err
			}
			return ch, nil
		})

		if err == nil {
			return rawRes.(<-chan StreamMessage), nil
		}

		lastErr = err
		if errors.Is(err, gobreaker.ErrOpenState) {
			log.Printf("🚨 [%s] 熔断器已开启，跳过流式重试", p.Name())
			continue
		}
		log.Printf("⚠️ [%s] 流式连接失败: %v，准备切换备用模型", p.Name(), err)
	}
	return nil, fmt.Errorf("所有模型流式连接均失败: %w", lastErr)
}