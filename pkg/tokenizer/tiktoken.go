package tokenizer
import(
	"log"

	"github.com/pkoukk/tiktoken-go"
)
var tkm *tiktoken.Tiktoken
func init(){
	// 🌟 初始化使用 cl100k_base 词表 (GPT-4 / DeepSeek 通用基础词表)
	// 这个词表会在内存里加载映射字典，所以放在 init() 里只加载一次，保证极高的性能
	var err error
	tkm, err = tiktoken.GetEncoding("cl100k_base")
	if err != nil{
		log.Fatalf("❌ 初始化 Tiktoken 分词器失败: %v", err)
	}
	log.Println("✅ 本地 Tiktoken 离线分词器加载完成")
}

// CountTokens 传入字符串，返回精准的 Token 数量
func CountTokens(text string) int {
	if text == ""{
		return 0
	}
	// Encode 方法会按照 BPE 算法将文本切分成 Token 数组
	tokens := tkm.Encode(text, nil, nil)
	return len(tokens)
}