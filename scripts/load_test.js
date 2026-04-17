import http from 'k6/http';
import { check } from 'k6';
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js';

export const options = {
    vus: 100,          // 模拟 100 个并发用户
    duration: '30s',   // 持续狂暴轰炸 30 秒
};

export default function () {
    const url = 'http://localhost:8080/v1/chat/completions';
    
    // 🌟 核心开关：
    // 如果你想测【缓存穿透】(打满 MySQL 和 LLM)，取消下面这行的注释：
    //  const promptText = `你好，这是一个固定的测试问题，测试缓存命中率`;
    
    // 如果你想测【缓存命中】(打满 Redis)，取消下面这行的注释，注释掉上面那行：
    // const promptText = `你好，这是一个固定的测试问题`; 
    const promptText = `压测随机问题_${uuidv4()}`;

    const payload = JSON.stringify({
        prompt: promptText,
        stream: false,
    });

    const params = {
        headers: {
            'Content-Type': 'application/json',
            'Authorization': 'Bearer test-api-key-123', // 刚才数据库里配的 Key
        },
    };

    const res = http.post(url, payload, params);
    
    // 检查是否返回 200 OK
    check(res, {
        'is status 200': (r) => r.status === 200,
    });
}