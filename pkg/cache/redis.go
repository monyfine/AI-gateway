package cache

import (
	"ai-gateway/pkg/llm"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
	// 🌟 统一超时时间
	timeout time.Duration
}

func NewRedisCache(ttl time.Duration) *RedisCache {
	rdb := redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS_ADDR"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
		PoolSize: 10,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		panic(fmt.Sprintf("Redis 连接失败: %v", err))
	}
	return &RedisCache{
		client:  rdb,
		ttl:     ttl,
		timeout: 10 * time.Second,
	}
}

func (c *RedisCache) SetAIResult(taskID, result string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel() // 🌟 关键：确保释放
	key := "ai:" + taskID
	return c.client.Set(ctx, key, result, c.ttl).Err()
}

func (c *RedisCache) GetAIResult(taskID string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	key := "ai:" + taskID
	val, err := c.client.Get(ctx, key).Result()
	if err == context.DeadlineExceeded {
		log.Printf("⚠️ Redis 读取超时 [%s]", taskID)
		return "", false
	}

	if err == redis.Nil {
		return "", false // 未命中
	}

	if err != nil {
		log.Printf("⚠️ Redis 错误: %v", err)
		return "", false
	}
	return val, true
}

func (c *RedisCache)IncrRetryCount(taskID string)(int, error){
	ctx, cancel := context.WithTimeout(context.Background(),c.timeout)
	defer cancel()
	key := "retry_cnt:" + taskID
	// 1. 原子递增
	val, err := c.client.Incr(ctx,key).Result()
	if err != nil{
		return 0,err
	}

	// 2. 如果是第一次递增（val==1），设置过期时间（兜底，防止内存泄漏）
	if val == 1{
		c.client.Expire(ctx, key, 24*time.Hour)
	}
	return int(val),nil
}

// GetRetryCount 获取当前重试次数
func (c *RedisCache) GetRetryCount(taskID string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	key := "retry_cnt:" + taskID
	val, err := c.client.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// ClearRetryCount 任务成功后清除计数器
func (c *RedisCache) ClearRetryCount(taskID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	key := "retry_cnt:" + taskID
	return c.client.Del(ctx, key).Err()
}

// RecordTokenUsage 记录 Token 消耗
func (c *RedisCache)RecordTokenUsage(taskID string,usage llm.Usage)error{
	ctx,cancel := context.WithTimeout(context.Background(),c.timeout)
	defer cancel()
	// 使用 Hash 存储，方便扩展（比如后续增加金额统计）
	key := "usage:" + taskID
	data := map[string]interface{}{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
		"recorded_at":       time.Now().Format(time.RFC3339),
	}
	return c.client.HSet(ctx,key,data).Err()
}

// GetTotalTokens 获取全局或特定维度的累计消耗（用于成本大盘）
func (c *RedisCache) IncrGlobalTokenStats(usage llm.Usage) {
	ctx := context.Background()
	// 原子递增全局消耗
	c.client.IncrBy(ctx, "stats:total_prompt_tokens", int64(usage.PromptTokens))
	c.client.IncrBy(ctx, "stats:total_completion_tokens", int64(usage.CompletionTokens))
}

func (c *RedisCache)Close()error{
	return c.client.Close()
}