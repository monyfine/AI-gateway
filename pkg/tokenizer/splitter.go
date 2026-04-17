package tokenizer

// SplitTextWithOverlap 带有重叠区间的精确 Token 切分
// maxTokens: 每个块的最大 Token 数
// overlapTokens: 相邻两个块的重叠 Token 数（设为 maxTokens 的 5%~10%）
func SplitTextWithOverlap(text string, maxTokens int, overlapTokens int)[]string {
	if text == "" {
		return nil
	}

	// 1. 将整段文本编码为 Token 数组 (复用 tiktoken.go 中初始化的 tkm)
	tokens := tkm.Encode(text, nil, nil)
	totalTokens := len(tokens)

	// 如果总长度没超，直接返回
	if totalTokens <= maxTokens {
		return[]string{text}
	}

	var chunks[]string
	
	// 步长 = 最大长度 - 重叠长度
	//一般来说是不会的重叠长度是最大长度的5%-10%左右
	//但是可能是0最大长度很小时，不过一般会进入fast队列
	step := maxTokens - overlapTokens
	if step <= 0 {
		step = maxTokens / 2 // 兜底防死循环
	}

	// 2. 滑动窗口切分
	for i := 0; i < totalTokens; i += step {
		end := i + maxTokens
		if end > totalTokens {
			end = totalTokens
		}

		// 将截取出来的 Token 数组解码回字符串
		chunkStr := tkm.Decode(tokens[i:end])
		chunks = append(chunks, chunkStr)

		// 如果已经切到了末尾，结束循环
		if end == totalTokens {
			break
		}
	}

	return chunks
}