# 阶段 1：编译环境
FROM golang:1.22-alpine AS builder

ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOPROXY=https://goproxy.cn,direct

WORKDIR /app

# 利用缓存拉取依赖
COPY go.mod go.sum ./
RUN go mod download

# 复制代码并分别编译 API 和 Worker 两个二进制文件
COPY . .
RUN go build -ldflags="-w -s" -o api-server ./cmd/api/main.go
RUN go build -ldflags="-w -s" -o worker-server ./cmd/worker/main.go

# 阶段 2：运行环境
FROM alpine:latest
WORKDIR /app

RUN apk add --no-cache tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone

# 把编译好的两个文件都拷过来
COPY --from=builder /app/api-server .
COPY --from=builder /app/worker-server .
COPY --from=builder /app/.env . 

# 暴露 API 端口
EXPOSE 8080 9091