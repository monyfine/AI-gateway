package cache

import (
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

func (c *RedisCache)Close()error{
	return c.client.Close()
}