package main

import (
	"io"
	"log"
	"net/http"
)

func main() {
	// 定义回调接收路径
	http.HandleFunc("/api/v1/articles/callback", func(w http.ResponseWriter, r *http.Request) {
		// 1. 读取 Worker 发过来的 AI 结果
		body, _ := io.ReadAll(r.Body)
		
		log.Println("============================================")
		log.Printf("✅ 收到 AI 网关的回调！数据如下：\n%s\n", string(body))
		log.Println("============================================")

		// 2. 给 Worker 回复一个 200 OK，告诉它你收到了
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Success"))
	})

	log.Println("🚀 临时主系统（Mock Server）已启动，正在监听 :8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}