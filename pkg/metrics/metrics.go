package metrics // 🌟 确保包名是 metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 🌟 确保变量名首字母大写 L
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
)