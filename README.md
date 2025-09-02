# zai2api

## 项目简介

为 Z.ai 提供 OpenAI API 兼容接口的代理服务，允许开发者通过标准的 OpenAI API 格式访问 Z.ai 的 GLM-4.5 模型。

## 主要特性

- **OpenAI API 兼容**：支持标准的 `/v1/chat/completions` 和 `/v1/models` 端点
- **流式响应支持**：完整实现 Server-Sent Events (SSE) 流式传输
- **思考内容处理**：提供多种策略处理模型的思考过程（`<details>` 标签）
- **匿名会话支持**：可选使用匿名 token 避免共享对话历史
- **调试模式**：详细的请求/响应日志记录
- **CORS 支持**：内置跨域资源共享支持
- **Docker 支持**：提供 Dockerfile 和 docker-compose 配置
- **环境变量配置**：支持通过环境变量灵活配置所有参数

## 使用场景

- 将 Z.ai 集成到支持 OpenAI API 的应用程序中
- 开发需要同时使用多个 AI 服务的应用
- 测试和评估 GLM-4.5 模型的能力

## 快速开始

### 方式一：使用 Docker Compose（推荐）

1. 克隆仓库：
   ```bash
   git clone https://github.com/yourusername/zai2api.git
   cd zai2api
   ```

2. 添加环境变量，修改
  ```bash
  cp .env.example .env
  ```

3. 启动服务：
   ```bash
   # 构建并启动
   docker-compose up -d
   
   # 查看日志
   docker-compose logs -f
   
   # 停止服务
   docker-compose down
   ```

## API 使用示例

### Python 示例
```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:3007/v1",
    api_key="sk-123456"  # 使用你配置的 DEFAULT_KEY
)

# 非流式调用
response = client.chat.completions.create(
    model="GLM-4.5",
    messages=[{"role": "user", "content": "你好"}],
    stream=False
)
print(response.choices[0].message.content)

# 流式调用
stream = client.chat.completions.create(
    model="GLM-4.5",
    messages=[{"role": "user", "content": "介绍一下人工智能"}],
    stream=True
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")
```

### cURL 示例
```bash
# 获取模型列表
curl -X GET http://localhost:3007/v1/models \
  -H "Authorization: Bearer sk-123456"

# 非流式聊天
curl -X POST http://localhost:3007/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-123456" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'

# 流式聊天
curl -X POST http://localhost:3007/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-123456" \
  -d '{
    "model": "GLM-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }' --no-buffer
```

## 配置选项

所有配置项都支持通过环境变量设置：

| 配置项 | 描述 | 默认值 |
|--------|------|--------|
| `UPSTREAM_URL` | Z.ai 的上游 API 地址 | `https://chat.z.ai/api/chat/completions` |
| `DEFAULT_KEY` | 下游客户端鉴权 key | `sk-123456` |
| `UPSTREAM_TOKEN` | 上游 API 的 token | (默认 token) |
| `MODEL_NAME` | 显示的模型名称 | `GLM-4.5` |
| `PORT` | 服务监听端口 | `:3007` |
| `DEBUG_MODE` | 调试模式开关 | `true` |
| `THINK_TAGS_MODE` | 思考内容处理策略 | `strip` (可选: `think`, `raw`) |
| `ANON_TOKEN_ENABLED` | 是否使用匿名 token | `true` |

### 思考内容处理策略说明

- `strip`: 去除 `<details>` 标签，不显示思考过程
- `think`: 将 `<details>` 标签转换为 `<think>` 标签
- `raw`: 保留原始的 `<details>` 标签

## 许可证

LICENSE

## 免责声明

本项目与 Z.ai 官方无关，使用前请确保遵守 Z.ai 的服务条款。
