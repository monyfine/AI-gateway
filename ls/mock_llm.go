// mock_llm.go
package main
import (
	"net/http"
	// "time"
)
func main() {
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// time.Sleep(500 * time.Millisecond) // 模拟大模型思考时间
		w.Header().Set("Content-Type", "application/json")
		// 模拟返回 OpenAI 格式的数据，包含 Usage
		w.Write([]byte(`{"choices":[{"message":{"content":"这是Mock的AI回答"}}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`))
	})
	http.ListenAndServe(":9999", nil)
}