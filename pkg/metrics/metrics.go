package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	LLMLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ai_gateway_llm_latency_seconds",
		Help:    "Latency of LLM API calls",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	}, []string{"provider"})

	TasksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_gateway_tasks_total",
		Help: "Total number of AI tasks processed",
	}, []string{"status"})

	KafkaLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ai_gateway_kafka_lag",
		Help: "Current message lag in Kafka topic",
	}, []string{"topic", "partition"})

	//记录 API 请求总量和状态码
	APIRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_gateway_api_requests_total",
		Help: "Total number of API requests",
	}, []string{"method", "path", "status"})

	//记录触发限流的次数
	RateLimitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ai_gateway_rate_limit_total",
		Help: "Total number of rate limit hits",
	}, []string{"app_name", "limit_type"}) // limit_type 可以是 rpm 或 tpm
)
