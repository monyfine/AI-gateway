package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// llm 包不关心具体的缓存实现，只要满足这两个方法的对象都能传进来
type SubTaskCache interface {
	GetCachedResponse(prompt string) (string, bool)
	SetCachedResponse(prompt string, response string) error
}

type MapReduceEngine struct {
	router *LLMRouter
	cache  SubTaskCache // 🌟 将具体的 *cache.RedisCache 替换为接口
}

// 构造函数接收接口
func NewMapReduceEngine(router *LLMRouter, cache SubTaskCache) *MapReduceEngine {
	return &MapReduceEngine{
		router: router,
		cache:  cache,
	}
}

// ProcessLargeTask 执行带 Overlap 和 Tree Reduce 的大任务处理
func (e *MapReduceEngine) ProcessLargeTask(ctx context.Context, instruction string, chunks[]string) (string, Usage, error) {
	var totalUsage Usage
	var mu sync.Mutex

	// ==========================================
	// 阶段 1：Map (并发处理所有初始子块)
	// ==========================================
	mapResults, mapUsage, err := e.executeBatch(ctx, instruction, chunks, true)
	if err != nil {
		return "", totalUsage, fmt.Errorf("Map 阶段失败: %w", err)
	}
	
	mu.Lock()
	totalUsage.add(mapUsage)
	mu.Unlock()

	// ==========================================
	// 阶段 2：Tree Reduce (层级规约)
	// ==========================================
	// 每次最多把 5 个结果合并成 1 个
	const ReduceBatchSize = 5
	currentChunks := mapResults

	level := 1
	for len(currentChunks) > 1 {
		var nextLevelChunks[]string

		// 将当前层的块按 ReduceBatchSize 分组
		for i := 0; i < len(currentChunks); i += ReduceBatchSize {
			end := i + ReduceBatchSize
			if end > len(currentChunks) {
				end = len(currentChunks)
			}
			
			batchStr := strings.Join(currentChunks[i:end], "\n\n---\n\n")
			nextLevelChunks = append(nextLevelChunks, batchStr)
		}

		// 并发执行这一层的 Reduce
		reduceResults, reduceUsage, err := e.executeBatch(ctx, instruction, nextLevelChunks, false)
		if err != nil {
			return "", totalUsage, fmt.Errorf("Tree Reduce 第 %d 层失败: %w", level, err)
		}

		mu.Lock()
		totalUsage.add(reduceUsage)
		mu.Unlock()

		// 晋级到下一层
		currentChunks = reduceResults
		level++
	}

	// 最终剩下的唯一一个块，就是全局最终结果
	return currentChunks[0], totalUsage, nil
}

// executeBatch 内部并发执行器 (自带 Redis 断点续传)
func (e *MapReduceEngine) executeBatch(ctx context.Context, instruction string, chunks []string, isMapPhase bool) ([]string, Usage, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	results := make([]string, len(chunks))
	batchUsage := Usage{}
	var firstErr error

	limitChan := make(chan struct{}, 5)

	for i, chunk := range chunks {
		wg.Add(1)
		limitChan <- struct{}{}
		go func(index int, text string) {
			defer func() { <-limitChan }()
			defer wg.Done()

			var prompt string
			if isMapPhase {
				prompt = fmt.Sprintf(`【系统指令】：你是一个客观的信息提取引擎。以下文本是一份超长文档的【第 %d 部分】片段。
请严格遵循以下规则：
1. 仅根据提供的片段内容进行处理，绝不能引入外部知识。
2. 不要得出最终结论，因为你只看到了局部信息。
3. 保持客观，提取核心事实、数据和关键逻辑。
4. 用户的原始处理指令是：“%s”。

【文档片段】：
%s`, index+1, instruction, text)
			} else {
				prompt = fmt.Sprintf(`【系统指令】：你是一个高级内容整合专家。以下是针对同一份长文档的多个【连续片段的总结】。
请严格遵循以下规则：
1. 将这些片段的内容融合成一份全局的、逻辑连贯的最终结果。
2. 消除不同片段之间重复或冗余的信息。
3. 修复因文本截断导致的上下文不连贯问题。
4. 用户的原始处理指令是：“%s”。

【连续片段总结】：
%s`, instruction, text)
			}

			//核心优化：子任务断点续传 (查缓存)
			if cachedRes, ok := e.cache.GetCachedResponse(prompt); ok {
				mu.Lock()
				results[index] = cachedRes
				mu.Unlock()
				return // 🚀 命中缓存，直接返回，不调大模型！
			}

			// 缓存未命中，真正调用大模型
			res, usage, err := e.router.InvokeWithFallback(ctx, prompt)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}

			// 🌟 核心优化：子任务成功后，立刻写入缓存，保住胜利果实
			_ = e.cache.SetCachedResponse(prompt, res)

			results[index] = res
			batchUsage.PromptTokens += usage.PromptTokens
			batchUsage.CompletionTokens += usage.CompletionTokens
			batchUsage.TotalTokens += usage.TotalTokens
		}(i, chunk)
	}

	wg.Wait()
	return results, batchUsage, firstErr
}

// 辅助方法：累加 Usage
func (u *Usage) add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
}