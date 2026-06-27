#!/usr/bin/env bash

# 周报生成系统启动脚本
# 用法: ./run.sh
# 支持从 .env 文件自动加载环境变量

set -e

cd "$(dirname "$0")"

if [ -f .env ]; then
    echo "✅ 加载 .env 环境变量"
    set -a
    source .env
    set +a
else
    echo "⚠️  .env 文件不存在，将使用 config.yaml 中的硬编码值"
fi

echo "🚀 启动周报生成系统..."
go run cmd/server/main.go
