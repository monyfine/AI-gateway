package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var rdb *redis.Client

func main() {
    rdb = redis.NewClient(&redis.Options{
        Addr:     "localhost:6379", // Redis 地址
        Password: "",               // 密码（默认无）
        DB:       0,                // 数据库编号（0-15）
        PoolSize: 10,              // 连接池大小
    })

    // 测试连接
    ctx := context.Background()
    if err := rdb.Ping(ctx).Err(); err != nil {
        panic(fmt.Sprintf("Redis 连接失败: %v", err))
    }
    fmt.Println("✅ Redis 连接成功")
	err := rdb.Set(ctx,"key","value",10*time.Minute).Err()
	val, err := rdb.Get(ctx,"key").Result()
	if err ==redis.Nil{
		fmt.Println("key is not")
	}else if err != nil{
		panic(err)
	}else{
		fmt.Println("val=",val)
	}
	rdb.Del(ctx,"key")
}