# Stream Runner

一个用于管理和转发 RTMP 流的 Go 语言服务，支持多路流并发处理、自动重连、日志捕获和配置热重载。

## 功能特性

- ✅ **多路流管理**：支持同时管理多个 RTMP 流
- ✅ **自动重连**：流断开时自动重试连接
- ✅ **配置热重载**：支持 SIGHUP 信号动态重载配置，无需重启服务
- ✅ **日志捕获**：实时捕获并记录 ffmpeg 的输出日志，带时间戳和流ID
- ✅ **日志轮转**：自动管理日志文件大小和轮转（100MB，保留5个文件）
- ✅ **看门狗机制**：自动检测并重启异常停止的流
- ✅ **系统服务**：支持 systemd 服务管理
- ✅ **进程管理**：支持 PID 文件管理和进程组管理

## 系统要求

- Linux 系统（推荐）
- Go 1.21 或更高版本（用于构建）
- ffmpeg（运行时必需）
- systemd（用于服务管理，可选）

## 安装和构建

### 本地开发

```bash
# 克隆项目
git clone <repository-url>
cd stream-runner

# 安装依赖
go mod tidy

# 构建
go build -o stream-runner main.go
```

### 打包部署

使用 `deploy.rb` 脚本生成 Linux 二进制文件和部署包：

```bash
ruby deploy.rb
```

这将生成：
- `stream-runner.tar.gz` - 完整的部署包
- 包含 Linux 二进制文件、配置文件、systemd 服务文件和部署脚本

## 配置说明

配置文件位于 `/etc/stream-runner/streams.yaml`，格式如下：

```yaml
streams:
  - id: stream-1
    src: rtmp://source-server.com/live/stream1
    dst: rtmp://127.0.0.1:1936/live/stream1
  - id: stream-2
    src: rtmp://source-server.com/live/stream2
    dst: rtmp://127.0.0.1:1936/live/stream2
```

### 配置项说明

- `id`: 流的唯一标识符
- `src`: 源 RTMP 流地址
- `dst`: 目标 RTMP 流地址

## 使用方法

### 直接运行

```bash
# 确保配置文件存在
sudo mkdir -p /etc/stream-runner
sudo cp config/streams.yaml /etc/stream-runner/

# 运行服务
sudo ./stream-runner
```

### 作为系统服务运行

```bash
# 解压部署包
tar -xzf stream-runner.tar.gz
cd stream-runner-pkg

# 运行部署脚本
sudo ./deploy.sh
```

部署脚本会自动：
1. 停止旧服务（如果存在）
2. 复制二进制文件和配置文件
3. 安装 systemd 服务
4. 启动服务
5. 等待 3 秒后测试 API 端点

### 服务管理

```bash
# 启动服务
sudo systemctl start stream-runner

# 停止服务
sudo systemctl stop stream-runner

# 重启服务
sudo systemctl restart stream-runner

# 查看状态
sudo systemctl status stream-runner

# 查看日志
sudo journalctl -u stream-runner -f
```

## 配置热重载

服务支持通过 SIGHUP 信号动态重载配置，无需重启：

```bash
# 重载配置
sudo systemctl reload stream-runner

# 或使用 kill 命令
sudo kill -HUP $(cat /var/run/stream-runner.pid)
```

重载时会：
- 停止已删除的流
- 启动新增的流
- 更新配置变更的流

## 日志管理

### 日志位置

- 主日志文件：`/var/log/stream-runner/stream.log`
- 轮转日志：`/var/log/stream-runner/stream.log.1`, `.2`, `.3`, `.4`, `.5`

### 日志格式

```
[2025-01-15 14:30:25] [stream-1] starting ffmpeg...
[2025-01-15 14:30:26] [stream-1] frame=  123 fps= 30 q=-1.0 size=    1024kB
[2025-01-15 14:30:27] [stream-2] starting ffmpeg...
```

每条日志包含：
- 时间戳：`[YYYY-MM-DD HH:MM:SS]`
- 流ID：`[stream-id]`
- 日志内容：ffmpeg 输出或系统消息

### 日志轮转

- 当日志文件达到 100MB 时自动轮转
- 保留最近 5 个日志文件
- 每小时检查一次是否需要轮转

## 信号处理

服务支持以下信号：

- `SIGHUP`: 重载配置文件
- `SIGINT` / `SIGTERM`: 优雅关闭服务，停止所有流

## 进程管理

- PID 文件：`/var/run/stream-runner.pid`
- 进程组：每个 ffmpeg 进程在独立的进程组中运行，便于管理

## 故障排查

### 检查 ffmpeg 是否安装

服务启动前会自动检查 ffmpeg 是否可用，如果不存在会直接退出。

### 查看服务状态

```bash
# 查看服务状态
sudo systemctl status stream-runner

# 查看详细日志
sudo tail -f /var/log/stream-runner/stream.log
```

### 常见问题

1. **流无法启动**
   - 检查源地址是否可访问
   - 检查目标地址是否正确
   - 查看日志文件获取详细错误信息

2. **配置重载失败**
   - 检查配置文件格式是否正确
   - 查看日志文件获取错误详情

3. **日志文件过大**
   - 日志会自动轮转，但可以手动清理旧日志
   - 修改 `MaxLogSize` 和 `MaxLogFiles` 常量调整轮转策略

## 开发

### 项目结构

```
stream-runner/
├── main.go          # 主程序
├── go.mod           # Go 模块定义
├── go.sum           # 依赖校验和
├── deploy.rb        # 部署脚本生成器
├── Rakefile         # Rake 任务定义
└── README.md        # 本文档
```

### 构建选项

```bash
# 本地构建
go build -o stream-runner main.go

# 交叉编译（Linux amd64）
GOOS=linux GOARCH=amd64 go build -o stream-runner main.go
```

## 许可证

MIT License

Copyright (c) 2025 kk

## 贡献

欢迎提交 Issue 和 Pull Request。

