package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	// 🌟 核心修改：路径必须和 .env 里的 CALLBACK_URL 路径部分完全一致
	http.HandleFunc("/api/v1/articles/callback", func(w http.ResponseWriter, r *http.Request) {
		// 读取一下内容，证明真的收到了
		body, _ := io.ReadAll(r.Body)
		fmt.Printf("📩 [主系统] 收到网关回调成功！内容长度: %d\n", len(body))
		fmt.Printf("📄 内容详情: %s\n", string(body))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"success"}`))
	})

	// 模拟 AI 的接口（如果你想切回模拟 AI 的话）
	http.HandleFunc("/mock-llm", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("🤖 收到 AI 调用请求")
		fmt.Fprint(w, `{
			"choices": [{"message": {"content": "这是模拟的 AI 回复"}}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
		}`)
	})

	fmt.Println("🛠️ Mock Server 运行在 :8080")
	fmt.Println("监听路径: /api/v1/articles/callback")
	http.ListenAndServe(":8080", nil)
}
