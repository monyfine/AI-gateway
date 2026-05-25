package llm

import (
	"ai-gateway/pkg/metrics"
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/sony/gobreaker"
)

type LLMRouter struct {
	modelProviders map[string][]Provider
	breakers       map[string]*gobreaker.CircuitBreaker
}

func NewLLMRouter(providers ...Provider) *LLMRouter {
	modelProviders := make(map[string][]Provider)
	breakers := make(map[string]*gobreaker.CircuitBreaker)
	for _,p := range providers{
		for _,modelName := range p.Models(){
			modelProviders[modelName] = append(modelProviders[modelName], p)
		}
		cbSettings := gobreaker.Settings{
			Name:        p.Name(),
			MaxRequests: 3,
			Interval:    5 * time.Second,
			Timeout:     10 * time.Second,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= 3
			},
		}
		breakers[p.Name()] = gobreaker.NewCircuitBreaker(cbSettings)
	}
	return &LLMRouter{
		modelProviders: modelProviders,
		breakers:       breakers,
	}
}

// InvokeWithFallback 带有模型路由、3次重试、熔断与监控的同步调用
func (r *LLMRouter) InvokeWithFallback(ctx context.Context, model string, prompt string) (string, Usage, error) {
	// 1. 按模型匹配对应的渠道列表
	providers, ok := r.modelProviders[model]
	if !ok || len(providers) == 0 {
		return "", Usage{}, fmt.Errorf("不支持的模型: %s", model)
	}

	var lastErr error

	for _, p := range providers {
		cb := r.breakers[p.Name()]

		// 定义一个“书包”结构体，用来装我们要带出来的两个宝贝
		type resultWrapper struct {
			content string
			usage   Usage
			err     error // 用于装载“不计入熔断”的特殊错误
		}

		// 内部重试 3 次
		for attempt := 1; attempt <= 3; attempt++ {
			start := time.Now()

			// 🌟 这里的匿名函数必须只返回 (interface{}, error)
			rawRes, err := cb.Execute(func() (interface{}, error) {
				// 🌟 核心修改：底层调用时带上 model 参数
				content, usage, invokeErr := p.Invoke(ctx, model, prompt) 
				if invokeErr != nil {
					if errors.Is(invokeErr, context.Canceled) {
						return resultWrapper{err: invokeErr}, nil
					}
					return nil, invokeErr // 失败时返回 nil 和 error
				}
				// 🌟 关键：把 content 和 usage 塞进书包，作为一个整体 (interface{}) 返回
				return resultWrapper{content: content, usage: usage}, nil
			})

			// 记录监控指标 (建议给 metric 加一个 model 标签，方便后续统计模型维度的耗时)
			metrics.LLMLatency.WithLabelValues(p.Name()).Observe(time.Since(start).Seconds())

			if err == nil {
				// 🌟 成功了！把书包打开，取出里面的东西
				data := rawRes.(resultWrapper)
				// 如果“书包”里装的是用户取消错误，直接返回，不再尝试当前渠道和其他渠道
				if data.err != nil {
					return "", Usage{}, data.err
				}

				return data.content, data.usage, nil
			}

			// --- 失败处理逻辑 ---
			lastErr = err

			// 如果熔断器开了，直接换下一个供应商，别试了
			if errors.Is(err, gobreaker.ErrOpenState) {
				log.Printf("🚨 [%s] 调用模型 [%s] 熔断器已开启，跳过重试", p.Name(), model)
				break
			}

			log.Printf("⚠️ [%s] 调用模型 [%s] 第 %d 次尝试失败: %v", p.Name(), model, attempt, err)
			if attempt < 3 {
				time.Sleep(time.Second * time.Duration(attempt))
			}
		}
	}

	return "", Usage{}, fmt.Errorf("模型 %s 的所有渠道全线崩溃，最后错误: %w", model, lastErr)
}

// InvokeStreamWithFallback 带有模型路由、熔断机制的流式调用
func (r *LLMRouter) InvokeStreamWithFallback(ctx context.Context, model string, prompt string) (<-chan StreamMessage, error) {
	// 1. 按模型匹配对应的渠道列表
	providers, ok := r.modelProviders[model]
	if !ok || len(providers) == 0 {
		return nil, fmt.Errorf("不支持的模型: %s", model)
	}

	var lastErr error

	for _, p := range providers {
		cb := r.breakers[p.Name()]

		rawRes, err := cb.Execute(func() (interface{}, error) {
			// 🌟 核心修改：流式调用也加上 model 参数
			ch, streamErr := p.InvokeStream(ctx, model, prompt)
			if streamErr != nil {
				if errors.Is(streamErr, context.Canceled) {
					// 用户取消，视为执行成功（不触发熔断），但返回错误给上层
					return nil, streamErr 
				}
				// 🚨 致命 Bug 修复保留：必须 return nil, err 避免死锁
				return nil, streamErr
			}
			return ch, nil
		})

		// 只有在 cb.Execute 认为 err == nil (即没有触发熔断机制拦截，且业务没有返回需要熔断的错) 时
		if err == nil {
			// 需要特判一下因为 ContextCanceled 被我们放行出来的错误
			if rawRes == nil {
				// 说明是 context.Canceled
				return nil, context.Canceled
			}
			return rawRes.(<-chan StreamMessage), nil
		}

		lastErr = err
		if errors.Is(err, gobreaker.ErrOpenState) {
			log.Printf("🚨 [%s] 模型 [%s] 熔断器已开启，跳过流式重试", p.Name(), model)
			continue
		}
		log.Printf("⚠️ [%s] 模型 [%s] 流式连接失败: %v，准备切换备用渠道", p.Name(), model, err)
	}
	
	return nil, fmt.Errorf("模型 %s 的所有渠道流式连接均失败: %w", model, lastErr)
}

// RouterManager 用于安全地管理和热替换 LLMRouter
type RouterManager struct {
	// 题目 1.1：声明一个 atomic.Value 类型的变量，命名为 current
	current atomic.Value
}

func NewRouterManager(initialRouter *LLMRouter) *RouterManager {
	rm := &RouterManager{}
	// 题目 1.2：使用 Store 方法，将 initialRouter 存入 current 中
	rm.current.Store(initialRouter)
	return rm
}

func (rm *RouterManager) Get() *LLMRouter {
	// 题目 1.3：使用 Load 方法取出值，并使用类型断言将其转换为 *LLMRouter 返回
	return rm.current.Load().(*LLMRouter)
}

func (rm *RouterManager) Reload(newRouter *LLMRouter) {
	// 题目 1.4：使用 Store 方法，将 newRouter 存入 current 中，完成热替换
	rm.current.Store(newRouter)
}