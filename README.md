# Stream Runner

一个用于管理和转发 RTMP 流的 Go 语言服务，支持多路流并发处理、自动重连、日志捕获和配置热重载。

## 功能特性

- 多路 RTMP 拷贝（ffmpeg `-c copy`）
- 自动重连：短失败 1s→30s 退避；连续播放 ≥20s 后再断则 1s 重拉
- SIGHUP / `systemctl reload` 热重载配置
- JSON 服务日志按日轮转（默认保留 30 天）；ffmpeg 输出进 journal
- systemd 打包（deb/rpm）+ PID / 进程组管理

## 系统要求

- Linux 系统（推荐）
- Go 1.27 或更高版本（用于构建）
- ffmpeg（运行时必需）
- systemd（用于服务管理，可选）

## 安装 / 卸载

Linux 一键（从 GitHub Release 拉 deb/rpm，失败再落 tar.gz）：

```bash
# 安装或覆盖升级
curl -fsSL https://raw.githubusercontent.com/kevin197011/stream-runner/main/deploy.sh | sudo bash

# 指定版本
SR_VERSION=v1.0.0 curl -fsSL https://raw.githubusercontent.com/kevin197011/stream-runner/main/deploy.sh | sudo bash

# 卸载（保留 /etc/stream-runner/streams.yml）
curl -fsSL https://raw.githubusercontent.com/kevin197011/stream-runner/main/deploy.sh | sudo bash -s -- --uninstall
```

本地仓库：

```bash
sudo ./deploy.sh
sudo ./deploy.sh --uninstall
```

编辑配置后热加载：

```bash
sudo nano /etc/stream-runner/streams.yml
sudo systemctl reload stream-runner
sudo systemctl status stream-runner
```

发版：`rake push` / `ruby push.rb` 在提交成功后自动打 semver tag 并推送 → GoReleaser 上传 deb/rpm/tar.gz。  
只要推代码不发版：`SKIP_TAG=1 rake push`。升 minor/major：`KK_GIT_BUMP=minor rake push`。

### 本地开发

```bash
git clone https://github.com/kevin197011/stream-runner.git
cd stream-runner
go mod tidy
go build -o stream-runner ./cmd/stream-runner
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
- 轮转日志：`/var/log/stream-runner/stream.log.YYYY-MM-DD`（默认保留 30 天）

### 日志格式

`stream.log` 是服务自己的 JSON 日志。ffmpeg 的警告和错误在进程标准输出上，systemd 下用 `journalctl -u stream-runner -f` 看。每行带时间戳和流 ID，不再重复打印 ffmpeg 版本横幅。

拉流成功后，每 10 秒在 `stream.log` 记一条源节奏。`behind_s` 是从这次拉流起，墙上时间比媒体时间多出来的秒数：大于 0 表示源送得比实时慢，落后在累积。达到 3 秒，或两次采样间隔超过 20 秒，记 `source lagging`。`speed` 是 ffmpeg 的输出倍速，明显低于 `1.0x` 同样说明源在拖。

同一条里带拉流路径：`src_host`、`src_ip`、`resolve_ms`（解析耗时）、`dial_ms`（连上该 IP 的 TCP 握手耗时）。解析或握手失败多一个 `dial_error`。握手达到 300 毫秒或失败，而媒体时钟还正常时，记 `pull network`。ffmpeg 一退出也会再探一次并写在 `ffmpeg exited` 上，所以源站还没连上、没有进度时也能看到网络结果。`behind_s` 变大而 `dial_ms` 很小，是源没在送；两个一起变大，网络也慢。这不是正在拉流那条连接的内核 RTT，也不是摄像机到画面的端到端延迟。

### 日志轮转

- 按本地日历日轮转：跨日时把当前 `stream.log` 改名为 `stream.log.YYYY-MM-DD` 并重新打开
- 默认保留最近 30 天的按日日志（常量 `LogKeepDays`）
- 每小时检查一次；启动时也会检查并清理过期文件

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

3. **日志磁盘占用**
   - 按日轮转后会自动删掉超过保留天数的文件
   - 修改 `LogKeepDays` 调整保留天数

## 开发

### 项目结构

```
stream-runner/
├── cmd/stream-runner/
├── internal/runner/
├── config/streams.yml
├── systemd/stream-runner.service
├── deploy.sh                  # 安装 / 卸载（含配置与 systemd）
├── .goreleaser.yaml
├── .github/workflows/release.yml
├── docs/REQUIREMENTS.md
└── README.md
```

详细需求见 [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)。

### 构建 / 发布

```bash
go build -o stream-runner ./cmd/stream-runner

# 本地试跑 GoReleaser（不上传）
goreleaser release --snapshot --clean

# 正式发版
git tag -a v1.0.1 -m "Release v1.0.1"
git push origin v1.0.1
```

## 许可证

MIT License

Copyright (c) 2025 kk

## 贡献

欢迎提交 Issue 和 Pull Request。
