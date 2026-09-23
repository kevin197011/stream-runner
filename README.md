# Stream Runner

一个用于管理和转发 RTMP 流的 Go 语言服务，支持多路流并发处理、自动重连、日志捕获和配置热重载。

## 功能特性

- ✅ **多路流管理**：支持同时管理多个 RTMP 流
- ✅ **自动重连**：ffmpeg 退出后自动再拉。源打不开时重试从 1 秒退避到 30 秒，避免源离线时每隔几秒拉起一次进程。连续播放超过 20 秒后再断，1 秒后重拉
- ✅ **配置热重载**：支持 SIGHUP 信号动态重载配置，无需重启服务
- ✅ **日志捕获**：ffmpeg 警告和错误打到标准输出（systemd 下看 journal），带时间戳和流 ID。服务自己的日志是 JSON，写在 `stream.log`
- ✅ **日志轮转**：`stream.log` 超过 100MB 轮转，保留 5 个文件，轮转后重新打开文件
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

### 从 GitHub Releases 安装

访问 [Releases](https://github.com/yourorg/stream-runner/releases) 页面下载最新版本：

**Debian/Ubuntu:**
```bash
wget https://github.com/yourorg/stream-runner/releases/download/v1.0.0/stream-runner_1.0.0_amd64.deb
sudo dpkg -i stream-runner_1.0.0_amd64.deb
```

**RHEL/CentOS:**
```bash
wget https://github.com/yourorg/stream-runner/releases/download/v1.0.0/stream-runner-1.0.0-1.x86_64.rpm
sudo rpm -i stream-runner-1.0.0-1.x86_64.rpm
```

### 本地打包部署

使用 `scripts/deploy.rb` 脚本在本地生成 Linux 软件包（.deb 和 .rpm）：

```bash
ruby scripts/deploy.rb
```

这将生成：
- `dist/stream-runner_1.0.0_amd64.deb` - Debian/Ubuntu 软件包
- `dist/stream-runner-1.0.0-1.x86_64.rpm` - RHEL/CentOS 软件包

软件包包含：
- 二进制文件：`/usr/local/bin/stream-runner`
- 配置模板：`/usr/share/stream-runner/streams.yml`（仅当 `/etc/stream-runner/streams.yml` 不存在时复制过去，已有文件不覆盖）
- systemd 服务：`/etc/systemd/system/stream-runner.service`

#### 安装软件包

**Debian/Ubuntu:**
```bash
sudo dpkg -i dist/stream-runner_*.deb
```

**RHEL/CentOS:**
```bash
sudo rpm -i dist/stream-runner-*.rpm
```

安装后会自动：
- 启用 systemd 服务
- 创建 `/etc/stream-runner`
- 配置文件不存在时，从模板生成 `/etc/stream-runner/streams.yml`

#### 配置

安装后编辑配置文件：
```bash
sudo nano /etc/stream-runner/streams.yml
```

然后启动服务：
```bash
sudo systemctl start stream-runner
sudo systemctl status stream-runner
```

## 配置说明

配置文件位于 `/etc/stream-runner/streams.yml`，格式如下：

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
sudo cp config/streams.yml /etc/stream-runner/

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

`stream.log` 是服务自己的 JSON 日志。ffmpeg 的警告和错误在进程标准输出上，systemd 下用 `journalctl -u stream-runner -f` 看。每行带时间戳和流 ID，不再重复打印 ffmpeg 版本横幅。

拉流成功后，每 10 秒在 `stream.log` 记一条源节奏。`behind_s` 是从这次拉流起，墙上时间比媒体时间多出来的秒数：大于 0 表示源送得比实时慢，落后在累积。达到 3 秒，或两次采样间隔超过 20 秒，记 `source lagging`。`speed` 是 ffmpeg 的输出倍速，明显低于 `1.0x` 同样说明源在拖。

同一条里的 `dial_ms` 是到源站的一次 TCP 握手耗时（毫秒），用来看网络往返。握手失败会多一个 `dial_error`。`behind_s` 变大而 `dial_ms` 很小，是源没在送；两个一起变大，网络也慢。这不是正在拉流那条连接的内核 RTT，也不是摄像机到画面的端到端延迟。

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
├── main.go              # 主程序
├── go.mod               # Go 模块定义
├── go.sum               # 依赖校验和
├── nfpm.yaml            # nfpm 打包配置
├── scripts/             # 构建和部署脚本
│   ├── deploy.rb        # 本地打包脚本
│   └── nfpm/            # nfpm 安装/卸载脚本
├── .github/workflows/   # GitHub Actions 工作流
│   └── release.yml      # 自动构建和发布工作流
├── Rakefile             # Rake 任务定义
└── README.md            # 本文档
```

### 构建选项

```bash
# 本地构建
go build -o stream-runner main.go

# 交叉编译（Linux amd64）
GOOS=linux GOARCH=amd64 go build -o stream-runner main.go
```

### GitHub Actions

项目配置了 GitHub Actions 自动构建和发布：

- 在 push 到 main/develop 分支时自动构建
- 在推送 tag（格式：`v*`）时自动创建 Release 并上传软件包

#### 创建新版本发布

```bash
# 1. 更新版本号（在 nfpm.yaml 中）
# 2. 提交更改
git add .
git commit -m "chore: bump version to 1.0.1"

# 3. 创建并推送 tag
git tag -a v1.0.1 -m "Release v1.0.1"
git push origin v1.0.1
```

## 许可证

MIT License

Copyright (c) 2025 kk

## 贡献

欢迎提交 Issue 和 Pull Request。

