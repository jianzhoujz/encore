<p align="center">
  <h1 align="center">Encore</h1>
  <p align="center">
    <strong>别再为限流丢请求了。像 Pro 一样使用免费 AI API。</strong>
  </p>
  <p align="center">
    <a href="https://github.com/jianzhoujz/encore/releases"><img src="https://img.shields.io/github/v/release/jianzhoujz/encore?style=flat-square&color=blue" alt="Release"></a>
    <a href="https://github.com/jianzhoujz/encore/blob/main/LICENSE"><img src="https://img.shields.io/github/license/jianzhoujz/encore?style=flat-square" alt="License"></a>
    <a href="https://github.com/jianzhoujz/encore"><img src="https://img.shields.io/badge/platform-macOS-lightgrey?style=flat-square" alt="Platform"></a>
    <a href="https://github.com/jianzhoujz/encore"><img src="https://img.shields.io/badge/dependencies-zero-brightgreen?style=flat-square" alt="Dependencies"></a>
  </p>
  <p align="center">
    <a href="./README.md">English</a>
  </p>
</p>

---

## 痛点

**NVIDIA NIM** 这类平台为你免费开放了强大的 AI 模型 —— DeepSeek、LLaMA、Mistral 等等。但免费额度伴随着严苛的限流策略，一次密集请求就会触发 `429 Too Many Requests`，让你的工作流中断、AI Agent 崩溃、IDE 助手卡在半句话上。

你不应该花时间去哄 API。你的工具应该直接能用。

## 方案

**Encore** 是一个轻量级本地代理，部署在你的应用和上游 AI API 之间。当请求被限流（或遇到临时性服务端错误）时，Encore 会自动吸收失败、等待、并重试 —— 你的应用始终收到干净的成功响应，就像限流从未发生过。

```
┌──────────────┐         ┌─────────────┐         ┌──────────────┐
│   你的应用    │ ──────> │    Encore   │ ──────> │  NVIDIA NIM  │
│ (Claude Code│         │ :9090 /     │  429?   │  或其他 API   │
│  等)        │         │  :9091      │  重试！  │              │
└──────────────┘ <────── │ OpenAI /    │ <────── │              │
              200 OK     │ Anthropic   │         └──────────────┘
                         └─────────────┘
```

**不需要改一行代码。** 只需把应用的 API 地址指向对应的端口，完事。

## 安装

```bash
brew tap jianzhoujz/tap
brew install encore
```

升级：

```bash
brew update --force
brew upgrade encore
```

或从源码构建：

```bash
go build -o encore ./cmd/encore
```

## 快速开始

**1. 创建配置文件** `~/.config/encore/config.json`：

```json
{
  "server": { "host": "127.0.0.1", "openaiPort": 9090, "anthropicPort": 9091 },
  "log": { "consoleLevel": "info", "fileLevel": "debug" },
  "retry": { "maxRetries": 5, "retryInterval": 3000 },
  "activeProviders": { "openai": "nvidia-nim", "anthropic": "" },
  "providers": {
    "nvidia-nim": {
      "name": "NVIDIA NIM",
      "protocol": "openai",
      "baseUrl": "https://integrate.api.nvidia.com/v1",
      "apiKey": "nvapi-xxxxxxxxxxxx"
    }
  }
}
```

**2. 启动代理：**

```bash
encore start
```

**3. 把你的应用指向 Encore** —— 只需改一下 API 地址：

| 客户端 | 端口 | 配置方式 |
|---|---|---|
| **Claude Code** | Anthropic (`:9091`) | `ANTHROPIC_BASE_URL=http://127.0.0.1:9091` |
| **其他 OpenAI 兼容客户端** | OpenAI (`:9090`) | 将 base URL 设为 `http://127.0.0.1:9090/v1` |

把应用里的 API Key 删掉 —— Encore 会自动注入。

## 为什么选 Encore？

| | 没有 Encore | 有了 Encore |
|---|---|---|
| 触发限流 | 应用报错或崩溃 | 透明重试，应用无感知 |
| 服务器 502/503 | 请求丢失 | 自动重试 |
| 伪装错误（NIM） | 静默失败，输出异常 | 检测并重试 |
| API Key 管理 | 分散在各个应用里 | 一份配置，统一管理 |
| 协议切换 | 逐个重新配置 | 独立端口，零混淆 |

## 特性

- **智能重试** —— 处理 429、502、503、504、网络错误，甚至[伪装错误](#伪装错误检测)（NVIDIA NIM 在 HTTP 200 里返回错误信息）
- **双端口架构** —— OpenAI 和 Anthropic 各自独立端口，每个端口绑定一个协议，无需路径判断
- **自定义模型列表** —— 每个 provider 可配置本地 JSON 模型文件，客户端看到的模型由你决定
- **模型名称覆盖** —— 强制所有请求使用指定的模型名称，覆盖客户端请求体中的 `model` 字段
- **实时流式响应** —— SSE 逐块刷新，零缓冲延迟
- **终端原生重试通知** —— 请求重试或最终失败时发送 Claude Code 风格的 OSC 9 桌面通知；不支持的终端会静默忽略
- **多 Provider** —— 定义任意数量的上游，通过 `activeProviders` 按协议激活
- **零依赖** —— 纯 Go 标准库，单一静态二进制
- **Homebrew 支持** —— `brew install` 即装即用

## 客户端配置

### Claude Code

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:9091
```

### 其他客户端

任何 OpenAI 兼容工具 —— 将 base URL 设为 `http://127.0.0.1:9090/v1`，移除 API Key。

## 伪装错误检测

部分上游（特别是 NVIDIA NIM）偶尔会将错误伪装成 HTTP 200 返回 —— 状态码看起来正常，但 body 实际内容是 `"rate limit exceeded"` 之类的错误信息。大多数重试逻辑对此完全无能为力。

Encore 会检查短小的非流式 200 响应体，识别已知错误模式：`rate limit exceeded`、`too many requests`、`upstream connect error`、`gateway timeout`、`service unavailable` 等。一旦检测到，该请求会像真正的 429 一样被自动重试。

## 终端通知

当请求发生重试，或所有重试都失败时，Encore 会向当前控制终端写入 iTerm2 风格的 OSC 9 通知序列，和 Claude Code 的终端原生通知机制一致。iTerm2、Ghostty、Kitty、WezTerm、Warp、Rio 等终端可将其显示为桌面通知；不支持 OSC 9 的终端会直接忽略。

## 配置参考

Provider 字段（`name`、`protocol`、`baseUrl`、`apiKey`）为必填。`models` 和 `anthropicPort` 为可选。Encore 执行严格校验，缺什么会明确告诉你。

| 字段 | 说明 |
|---|---|
| `server.host` | 监听地址（如 `127.0.0.1`） |
| `server.openaiPort` | OpenAI 协议监听端口（如 `9090`） |
| `server.anthropicPort` | Anthropic 协议监听端口（如 `9091`）。`0` = 禁用 |
| `log.consoleLevel` | 控制台日志级别：`verbose` / `debug` / `info` / `error` |
| `log.fileLevel` | 文件日志级别（日志写入 `~/Library/Logs/encore/`） |
| `retry.maxRetries` | 单个请求最大重试次数 |
| `retry.retryInterval` | 重试间隔（毫秒） |
| `activeProviders.openai` | 激活的 OpenAI 协议 provider key（留空禁用） |
| `activeProviders.anthropic` | 激活的 Anthropic 协议 provider key（留空禁用） |
| `providers.*.name` | 显示名称 |
| `providers.*.protocol` | `openai` 或 `anthropic` |
| `providers.*.baseUrl` | 上游 base URL |
| `providers.*.apiKey` | 上游 API Key |
| `providers.*.models` | （可选）自定义模型列表 JSON 文件名，存放于配置目录 |
| `providers.*.overrideModel` | （可选）强制所有请求使用此模型名称，覆盖客户端的 `model` 字段 |

## 已测试平台

- **NVIDIA NIM** — DeepSeek V3.2、DeepSeek V4 系列（免费额度，约 40 RPM）
- **Claude Code**（Anthropic 协议）

## 许可证

MIT
