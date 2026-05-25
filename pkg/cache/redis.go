package cache

import (
	"ai-gateway/pkg/llm"
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// 检查 TPM 滑动窗口 Lua 脚本
// KEYS[1]: limit:tpm:sliding:sk-xxx
// ARGV[1]: 当前时间戳 (毫秒)
// ARGV[2]: 窗口大小 (毫秒)
// ARGV[3]: TPM 限制额度
const checkTpmLua = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])

-- 1. 清理过期数据
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)

-- 2. 获取窗口内所有记录
local members = redis.call('ZRANGE', key, 0, -1)
local total_tokens = 0

-- 3. 遍历解析 "uuid:tokens" 并累加
for _, member in ipairs(members) do
    local colon_index = string.find(member, ":")
    if colon_index then
        local tokens = tonumber(string.sub(member, colon_index + 1))
        if tokens then
            total_tokens = total_tokens + tokens
        end
    end
end

-- 4. 判断是否超限
if total_tokens >= limit then
    return 0 -- 超限
end
return 1 -- 放行
`

// 增加 TPM 消耗 Lua 脚本
// KEYS[1]: limit:tpm:sliding:sk-xxx
// ARGV[1]: 当前时间戳 (毫秒)
// ARGV[2]: 窗口大小 (毫秒)
// ARGV[3]: member (格式 "uuid:tokens")
const addTpmLua = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local member = ARGV[3]

redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, window)
return 1
`

// 滑动窗口 RPM 限流 Lua 脚本
// KEYS[1]: 限流的 Key (如 limit:rpm:sliding:sk-xxx)
// ARGV[1]: 当前时间戳 (毫秒)
// ARGV[2]: 窗口大小 (毫秒，如 60000)
// ARGV[3]: 限制的请求数
// ARGV[4]: 唯一请求ID (用于 ZSET 的 member，防止同一毫秒的请求被覆盖)
const slidingWindowLua = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]

-- 1. 清除窗口外的旧数据 (0 到 当前时间-窗口大小)
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)

-- 2. 获取当前窗口内的请求总数
local current_reqs = redis.call('ZCARD', key)

-- 3. 判断是否超限
if current_reqs >= limit then
    return 0 -- 触发限流
end

-- 4. 未超限，将当前请求加入 ZSET，并重置过期时间
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, window)
return 1 -- 放行
`

type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
	// 🌟 统一超时时间
	timeout       time.Duration
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

// 1. 检查 RPM (每分钟请求数) - 升级为滑动窗口算法
func (c *RedisCache) CheckRPMLimit(ctx context.Context, appKey string, limit int) (bool, error) {
	if limit <= 0 {
		return true, nil // <=0 表示不限流
	}

	// 题目 1：准备 Lua 脚本需要的参数
	// 1. 构造 Redis Key，格式为 "limit:rpm:sliding:" + appKey
	redisKey := "limit:rpm:sliding:" + appKey

	// 2. 获取当前时间的毫秒级时间戳 (提示: time.Now().UnixMilli())
	now := time.Now().UnixMilli()

	// 3. 窗口大小固定为 60000 毫秒 (1分钟)
	window := int64(60000)

	// 4. 生成一个 UUID 字符串作为本次请求的唯一 member (提示: uuid.New().String())
	member := uuid.New().String()

	redisCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	// 题目 2：执行 Lua 脚本
	// 使用 c.client.Eval 执行 slidingWindowLua 脚本。
	// 注意：KEYS 参数是一个[]string{redisKey}
	//       ARGV 参数是一个[]interface{}{now, window, limit, member}
	result, err := c.client.Eval(redisCtx, slidingWindowLua, []string{redisKey}, now, window, limit, member).Result()

	// 题目 3：处理结果与降级
	// 1. 如果 err 不为空，打印降级日志，并 return c.checkLocalLimit(appKey, limit), nil
	// 2. 如果 result.(int64) == 1，返回 true, nil
	// 3. 否则返回 false, nil
	if err != nil {
		log.Printf("🚨[降级] Redis 滑动窗口限流异常(%v)，租户 %s 切换至本地限流", err, appKey)
		return c.checkLocalLimit(appKey, limit), nil
	}
	if result.(int64) == 1 {
		return true, nil
	}

	return false, nil
}

// 本地 RPM 降级逻辑
func (c *RedisCache) checkLocalLimit(appKey string, limit int) bool {
	ratePerSecond := float64(limit) / 60.0
	limiter, _ := c.localLimiters.LoadOrStore(appKey, rate.NewLimiter(rate.Limit(ratePerSecond), limit/10+1)) // 桶大小稍微给点冗余
	return limiter.(*rate.Limiter).Allow()
}

func (c *RedisCache) CheckSlidingTPM(ctx context.Context, appKey string, limit int) (bool, error){
	if limit <= 0 {
		return true, nil // <=0 表示不限流
	}
	redisKey := "limit:tpm:sliding:" + appKey
	now := time.Now().UnixMilli()
	window := int64(60000)
	redisCtx,cancel := context.WithTimeout(ctx,200*time.Millisecond)
	defer cancel()
	result, err := c.client.Eval(redisCtx,checkTpmLua,[]string{redisKey},now,window,limit).Result()
	if err != nil{
		//本地降级
		log.Printf("⚠️ Redis CheckSlidingTPM 失败，触发降级放行 (appKey: %s): %v", appKey, err)
		return true,nil
	}

	if resInt, ok := result.(int64); ok {
		if resInt == 1 {
			return true, nil
		}
		return false, nil
	}
	return false,fmt.Errorf("Redis Lua 返回格式异常")
}

func (c *RedisCache) AddSlidingTPMUsage(appKey string, tokens int){
	if tokens == 0{
		return
	}
	go func ()  {
		redisCtx,cancel := context.WithTimeout(context.Background(),2*time.Second)
		defer cancel()
		redisKey := "limit:tpm:sliding:" + appKey
		member := fmt.Sprintf("%s:%d", uuid.New().String(), tokens)
		now := time.Now().UnixMilli()
		window := int64(60000)
		result,err := c.client.Eval(redisCtx,addTpmLua,[]string{redisKey},now,window,member).Result()
		if err != nil{
			log.Printf("🚨 TPM 累加失败: %v", err)
			return
		}
		if resInt, ok := result.(int64); ok && resInt == 1 {
			// log.Printf("✅ 成功异步记录 TPM 消耗 (appKey: %s, 消耗 Tokens: %d)", appKey, tokens) // 测试时可打开
		}
	}()
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
func (c *RedisCache) generatePromptKey(prompt string) string {
	// 使用 SHA-256 将任意长度的 Prompt 压缩成 64 位的固定哈希值
	hash := sha256.Sum256([]byte(prompt))
	return "prompt_cache:" + hex.EncodeToString(hash[:])
}

// GetCachedResponse 根据 Prompt 获取 AI 回答
func (c *RedisCache) GetCachedResponse(prompt string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	key := c.generatePromptKey(prompt)
	val, err := c.client.Get(ctx, key).Result()

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
func (c *RedisCache) SetCachedResponse(prompt string, response string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	key := c.generatePromptKey(prompt)
	// 设置较长的过期时间，比如 7 天 (根据业务需求调整)
	return c.client.Set(ctx, key, response, 7*24*time.Hour).Err()
}

// GetGlobalTokenStats 获取全局 Token 消耗统计
func (c *RedisCache) GetGlobalTokenStats() (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
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
