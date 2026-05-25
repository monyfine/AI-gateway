// --- START OF FILE cmd/api/main.go ---
package main

import (
	"ai-gateway/config"
	"ai-gateway/internal/api"
	"ai-gateway/internal/model"
	"ai-gateway/pkg/cache"
	"ai-gateway/pkg/llm"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)



func main() {
	log.Println("🚀 正在启动 AI Gateway API 服务...")
	config.LoadConfig()
	model.InitDB()

	initialRouter := api.BuildDynamicRouter()

	routerManager := llm.NewRouterManager(initialRouter)
	redisCache := cache.NewRedisCache(24 * time.Hour)

	r := api.SetupRouter(routerManager, redisCache)
	port := config.GetEnv("API_PORT", ":8080")
	
	// 启动 9091 监控专属端口
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		log.Println("📊 监控服务已启动，监听端口 :9091")
		if err := http.ListenAndServe(":9091", mux); err != nil {
			log.Fatalf("❌ 监控服务启动失败: %v", err)
		}
	}()
	
	// 1. 创建原生的 HTTP Server
	srv := &http.Server{
		Addr:    port,
		Handler: r,
	}
	go func() {
		log.Printf("✅ API 服务已启动，监听端口 %s", port)
		// ErrServerClosed 是正常关闭的信号，不需要报错
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ 服务启动失败: %v", err)
		}
	}()

	// 3. 设置系统信号监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("⚠️ 接收到关闭信号，准备优雅停机，不再接收新请求...")

	// 4. 设置 10 秒钟的超时时间，给正在处理的请求收尾
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("😱 API 服务强制退出:", err)
	}

	log.Println("✅ API 服务已安全、平滑地退出")
}
/*
后续改---------------------------------------------------------------------------------------

阶段一：核心漏洞修复与高可用改造（P0 级 - 必须优先做）
目标：保证系统在高并发下不崩溃、不超卖、数据不混乱。
1. 引入 Redis 预扣费机制 (Pre-deduction)
干了什么：废弃现有的“同步查 MySQL 余额 -> 放行 -> 异步扣费”逻辑。改为：请求到达时，使用 Redis Lua 脚本预先冻结估算的 Token 费用（如按 max_tokens 预扣）；请求结束后，计算实际消耗，多退少补；最后通过定时任务异步将 Redis 的余额同步回 MySQL。
解决的问题：彻底解决并发透支（超卖）漏洞。防止恶意用户利用时间差瞬间发起大量请求刷爆额度；同时将 MySQL 从核心请求链路上剥离，大幅降低 API 延迟。
2. 分布式缓存一致性改造 (Redis Pub/Sub)
干了什么：在 AuthMiddleware 中，保留本地 sync.Map 缓存，但引入 Redis 的发布/订阅（Pub/Sub）机制。当管理员在后台禁用某个 AppKey 或修改额度时，发布一条广播消息，所有网关节点监听到后，主动删除本地对应的缓存。
解决的问题：解决多节点部署下的数据不一致。防止被禁用的 Key 在本地缓存过期前（5分钟内）依然可以继续调用。
3. 智能重试与错误分类 (Smart Retry)
干了什么：修改 InvokeWithFallback 的重试逻辑。解析上游返回的 HTTP 状态码：如果是 400 (参数错) 或 401/403 (鉴权失败)，直接中断并返回给前端；只有遇到 429 (限流) 或 5xx (服务器错误) 时，才触发重试和渠道切换。
解决的问题：避免无效重试造成的资源浪费。防止因为用户传错了一个参数，导致网关把所有备用渠道都盲目请求一遍，白白增加系统负载。
阶段二：路由分发与协议扩展（P1 级 - 核心业务能力）
目标：让网关真正具备强大的流量调度能力和多模型兼容能力。
4. 实现真正的加权负载均衡 (Weighted Load Balancing)
干了什么：重写 BuildDynamicRouter。不再是简单的按权重排序遍历，而是实现 加权轮询 (Weighted Round-Robin) 或 平滑加权轮询 算法。例如渠道 A 权重 7，渠道 B 权重 3，网关会按 7:3 的比例将流量打散。
解决的问题：解决伪负载均衡导致的单点压力。避免所有流量永远只打向权重最高的单一渠道，真正实现多渠道的并发分流。
5. 引入协议适配层 (Protocol Translation)
干了什么：在 Provider 接口下实现不同的适配器（如 ClaudeClient, GeminiClient, OllamaClient）。网关对外统一接收 OpenAI 格式的 JSON，内部根据路由到的渠道，自动将请求体转换为目标厂商的格式，并将返回结果再转换回 OpenAI 格式。
解决的问题：打破供应商锁定 (Vendor Lock-in)。让你的前端或客户无需修改任何代码（继续用 OpenAI SDK），就能无缝切换使用 Claude 3.5 或国产大模型。
6. 增加并发度限流 (Concurrency Limiting)
干了什么：在 Redis 中为每个 AppKey 增加一个当前活跃请求计数器（请求进来 +1，结束 -1）。如果超过设定的最大并发数（如 5），直接返回 429 Too Many Requests。
解决的问题：防止长连接耗尽系统资源。大模型流式输出耗时很长，少数恶意或异常租户的大量并发请求会迅速耗尽网关的 Goroutine 和上游连接池。
阶段三：异步架构与可观测性升级（P1/P2 级 - 支撑高并发）
目标：提升系统的吞吐量，让线上问题排查变得简单。
7. 引入消息队列处理日志与计费 (MQ Integration)
干了什么：移除代码中直接 go func() { DB.Create(...) } 的逻辑。改为将请求日志和计费事件序列化为 JSON，发送到 Kafka 或 RabbitMQ。编写独立的 Worker 服务消费队列，批量写入 MySQL 或 Elasticsearch。
解决的问题：消除突发流量下的 OOM 和数据库宕机风险。实现真正的削峰填谷，保证网关主程序的绝对轻量和稳定。
8. 结构化日志与全链路追踪 (Tracing & Logging)
干了什么：引入 Zap 替换标准库 log，输出 JSON 格式日志。引入 OpenTelemetry，在请求入口生成 TraceID，并将其注入到 Context 中，贯穿鉴权、路由、上游请求、日志记录的全过程，并在 HTTP Header 中返回给前端。
解决的问题：解决线上问题难以排查的痛点。当某个请求耗时 30 秒时，你可以通过 TraceID 精准定位是网关限流耗时、网络耗时、还是大模型推理耗时。
阶段四：安全合规与商业化进阶（P2 级 - 企业级标准）
目标：达到对外售卖、SaaS 化运营的工业级标准。
9. 敏感数据加密与脱敏 (Data Security)
干了什么：
数据库中的 AppKey 改为哈希存储（类似密码的 bcrypt），前端只在创建时展示一次明文，后续只能看到 sk-xxxx...xxxx。
为租户增加“隐私模式”开关，开启后，网关的 RequestLog 不再记录 Prompt 和 Response 的具体内容，只记录 Token 消耗。
解决的问题：满足数据合规与隐私保护要求。防止数据库泄露导致所有客户的 API Key 被盗刷，保护客户的商业机密对话不被网关管理员窥探。
10. 引入语义缓存 (Semantic Caching)
干了什么：废弃简单的 SHA256 精确匹配缓存。引入轻量级 Embedding 模型（如 text-embedding-3-small）将用户的 Prompt 向量化，存入 Redisearch 或 Milvus。当新请求到来时，计算相似度，相似度 > 95% 则直接返回缓存。
解决的问题：大幅降低 API 成本并提升响应速度。解决用户多打一个标点符号或换行导致缓存失效的问题。
11. 内容风控拦截 (Content Moderation)
干了什么：在请求发给大模型之前，增加一个前置拦截器。调用第三方审核 API（如阿里云绿网）或本地敏感词库，对 Prompt 进行合规性检查。违规直接阻断。
解决的问题：防止上游账号被封禁。在国内运营或使用海外 API 时，防止用户恶意注入生成违规内容，保护你的底层渠道资产安全。
------------------------------------------------------------------------------------------------
再把他魔化成直接给用户一个网页地址
*/