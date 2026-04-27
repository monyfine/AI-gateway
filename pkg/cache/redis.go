package cache

import (
	"ai-gateway/pkg/llm"
	"context"
	"fmt"
	"log"
	"os"
	"time"
	"sync"

	"crypto/sha256"
	"encoding/hex"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)
// RPM 限流 Lua 脚本 (固定 60 秒窗口)
const rpmLuaScript = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local current = tonumber(redis.call("GET", key) or "0")

if current >= limit then
    return 0 -- 🌟 超过限额，直接拒绝，不增加计数！
end

redis.call("INCR", key)
if current == 0 then
    redis.call("EXPIRE", key, 60)
end
return 1
`
// ==========================================
// 🌟 新增：重试计数器的 Lua 脚本
// 逻辑：给 Key +1，如果加完以后结果是 1，说明是第一次加，设置 86400 秒(24小时)的过期时间
// ==========================================
const incrRetryLuaScript = `
local key = KEYS[1]
local current = redis.call("INCR", key)
if current == 1 then
    redis.call("EXPIRE", key, 86400)
end
return current
`

// TPM 累加 Lua 脚本 (保证 INCRBY 和 EXPIRE 的原子性)
const addTpmLuaScript = `
local key = KEYS[1]
local increment = tonumber(ARGV[1])
local current = redis.call("INCRBY", key, increment)
if current == increment then
    redis.call("EXPIRE", key, 60) -- 如果是第一次累加，设置 60 秒过期
end
return current
`

type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
	// 🌟 统一超时时间
	timeout time.Duration
	localLimiters sync.Map 
}

func NewRedisCache(ttl time.Duration) *RedisCache {
	rdb := redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS_ADDR"),
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
		PoolSize: 20,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		panic(fmt.Sprintf("Redis 连接失败: %v", err))
	}
	return &RedisCache{
		client:  rdb,
		ttl:     ttl,
		timeout: 5 * time.Second,
	}
}

// 1. 检查 RPM (每分钟请求数)
func (c *RedisCache) CheckRPMLimit(ctx context.Context, appKey string, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil// <=0 表示不限流
	}
	now := time.Now().Format("200601021504") // 精确到分钟
	redisKey := fmt.Sprintf("limit:rpm:%s:%s", appKey, now)

	redisCtx, cancel := context.WithTimeout(ctx,200*time.Millisecond)
	defer cancel()

	result, err := c.client.Eval(redisCtx, rpmLuaScript, []string{redisKey}, limit).Result()
	if err != nil{
		log.Printf("🚨 [降级] Redis RPM 限流异常(%v)，租户 %s 切换至本地限流", err, appKey)
		return c.checkLocalLimit(appKey, limit), nil
	}
	return result.(int64) == 1, nil
}
// 本地 RPM 降级逻辑
func (c *RedisCache) checkLocalLimit(appKey string, limit int) bool {
	ratePerSecond := float64(limit)/60.0
	limiter, _ := c.localLimiters.LoadOrStore(appKey, rate.NewLimiter(rate.Limit(ratePerSecond), limit/10+1)) // 桶大小稍微给点冗余
	return limiter.(*rate.Limiter).Allow()
}
// 2. 检查 TPM (每分钟 Token 数) - 仅检查是否已超标
func (c *RedisCache) CheckTPMLimit(ctx context.Context, appKey string, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil
	}

	now := time.Now().Format("200601021504")
	redisKey := fmt.Sprintf("limit:tpm:%s:%s", appKey, now)

	redisCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	// 获取当前分钟已使用的 Token 数
	currentStr, err := c.client.Get(redisCtx, redisKey).Result()
	if err == redis.Nil{
		return true, nil// 还没用过，放行
	}else if err != nil{
		log.Printf("🚨 [降级] Redis TPM 读取异常(%v)，默认放行", err)
		return true, nil // Redis 挂了，为了高可用，默认放行
	}

	var current int
	fmt.Sscanf(currentStr, "%d", &current)
	return current <= limit, nil
}
// 3. 累加 TPM 消耗 (在 AI 调用完成后异步执行)
func (c *RedisCache) AddTPMUsage(appKey string, tokens int) {
	if tokens <= 0 {
		return
	}
	// 异步执行，不阻塞主流程
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		now := time.Now().Format("200601021504")
		redisKey := fmt.Sprintf("limit:tpm:%s:%s", appKey, now)

		// 🌟 改造点：使用 Lua 脚本保证累加和设置过期的原子性
		err := c.client.Eval(ctx, addTpmLuaScript, []string{redisKey}, tokens).Err()
		if err != nil {
			log.Printf("⚠️ Redis TPM 累加失败 [%s]: %v", appKey, err)
		}
	}()
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

func (c *RedisCache) IncrRetryCount(taskID string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	key := "retry_cnt:" + taskID

	// 1. 原子递增
	val, err := c.client.Eval(ctx, incrRetryLuaScript, []string{key}).Result()
	if err != nil {
		return 0, err
	}

	// Lua 返回的是 int64，转换成 int
	return int(val.(int64)), nil
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
func (c *RedisCache) RecordTokenUsage(taskID string, usage llm.Usage) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	// 使用 Hash 存储，方便扩展（比如后续增加金额统计）
	key := "usage:" + taskID
	data := map[string]interface{}{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
		"recorded_at":       time.Now().Format(time.RFC3339),
	}
	return c.client.HSet(ctx, key, data).Err()
}

// GetTotalTokens 获取全局或特定维度的累计消耗（用于成本大盘）
func (c *RedisCache) IncrGlobalTokenStats(usage llm.Usage) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout) // 🌟 使用类里统一定义的 timeout
	defer cancel()
	// 原子递增全局消耗
	c.client.IncrBy(ctx, "stats:total_prompt_tokens", int64(usage.PromptTokens))
	c.client.IncrBy(ctx, "stats:total_completion_tokens", int64(usage.CompletionTokens))
}

func (c *RedisCache) Close() error {
	return c.client.Close()
}

// generatePromptKey 生成基于 Prompt 的唯一哈希 Key
func (c *RedisCache)generatePromptKey(prompt string)string{
	// 使用 SHA-256 将任意长度的 Prompt 压缩成 64 位的固定哈希值
	hash := sha256.Sum256([]byte(prompt))
	return "prompt_cache:"+hex.EncodeToString(hash[:])
}

// GetCachedResponse 根据 Prompt 获取 AI 回答
func (c *RedisCache)GetCachedResponse(prompt string)(string,bool){
	ctx,cancel := context.WithTimeout(context.Background(),c.timeout)
	defer cancel()

	key := c.generatePromptKey(prompt)
	val, err := c.client.Get(ctx,key).Result()

	if err == nil {
		return val, true //成功命中
	}

	if err == redis.Nil {
		//这是正常的“缓存未命中”
		return "", false 
	}

	// 只有真正的错误（如连接断开）才记录日志
	log.Printf("⚠️ Redis 系统错误: %v", err)
	return "", false
}

// SetCachedResponse 将 AI 的回答缓存起来
func (c *RedisCache)SetCachedResponse(prompt string,response string) error {
	ctx,cancel := context.WithTimeout(context.Background(),c.timeout)
	defer cancel()
	key := c.generatePromptKey(prompt)
	// 设置较长的过期时间，比如 7 天 (根据业务需求调整)
	return c.client.Set(ctx, key, response, 7*24*time.Hour).Err()
}

// GetGlobalTokenStats 获取全局 Token 消耗统计
func (c *RedisCache)GetGlobalTokenStats()(map[string]int64, error){
	ctx,cancel := context.WithTimeout(context.Background(),c.timeout)
	defer cancel()

	// 从 Redis 读取我们之前存的全局统计
	promptTokens, _ := c.client.Get(ctx, "stats:total_prompt_tokens").Int64()
	completionTokens, _ := c.client.Get(ctx, "stats:total_completion_tokens").Int64()
	return map[string]int64{
		"total_prompt_tokens":     promptTokens,
		"total_completion_tokens": completionTokens,
		"total_tokens":            promptTokens + completionTokens,
	}, nil
}