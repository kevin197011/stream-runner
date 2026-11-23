# Stream Runner 需求文档

## 背景与目标

用纯 Go（gortmplib）把多路 RTMP 源流 remux 到目标地址，连接断开后自动重拉；运维侧支持 systemd、SIGHUP 热重载与可轮转的 JSON 日志。

### 非目标

- 不做转码 / 协议转换（仅 remux / packet copy）
- 不提供 HTTP 管理 API 或 Web UI（仅 `/metrics` 刮取面）
- 不内置 RTMP 服务端

## 用户与权限

- 典型以 root + systemd 运行（写 `/var/run`、`/var/log/stream-runner`、读 `/etc/stream-runner`）
- 无多用户鉴权面

## 功能需求

| 编号     | 需求            | 验收标准                                                                                                                                                                       |
| -------- | --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| FR-SR-01 | 多路流并发      | `streams.yml` 中多条 `id/src/dst` 各自独立 relay goroutine                                                                                                                     |
| FR-SR-02 | 自动重连        | relay 退出后重拉；短跑失败退避 1s→30s；连续播放 ≥20s 后再断则 1s 重拉                                                                                                          |
| FR-SR-03 | 配置热重载      | `SIGHUP` / `systemctl reload`：停删改、启新增、换 src/dst；坏配置不改现网 worker                                                                                               |
| FR-SR-04 | 日志            | 服务 JSON → `stream.log`；relay 错误/节奏告警带 stream id                                                                                                                      |
| FR-SR-05 | 日志轮转        | 按本地日历日将 `stream.log` 归档为 `stream.log.YYYY-MM-DD`；默认保留 30 天；启动与每小时检查                                                                                   |
| FR-SR-06 | 源节奏观测      | 用媒体 PTS 相对墙钟算 `behind_s` / 近似 `speed`；慢握手或落后打 `source lagging` / `pull network`                                                                              |
| FR-SR-07 | systemd 打包    | deb/rpm 安装二进制、配置模板、unit；首次安装生成 `/etc` 配置且不覆盖已有                                                                                                       |
| FR-SR-08 | Prometheus 指标 | 默认 `METRICS_ADDR=:9108` 暴露 `/metrics`；空字符串关闭；含 `stream_up` / `relay_restarts_total` / `behind_seconds`（按 `stream_id`）及 Go/process collector；流删除时清 label |

## 非功能需求

- Go 1.27+；**无**运行时外部二进制依赖（不依赖 ffmpeg）
- 配置路径默认 `/etc/stream-runner/streams.yml`；可用 `STREAM_RUNNER_CONFIG` / `STREAM_RUNNER_LOG_DIR` / `STREAM_RUNNER_PID` 覆盖
- PID：`/var/run/stream-runner.pid`；Stop 取消 in-process relay（关 RTMP 连接）
- 指标：`METRICS_ADDR` 默认 `:9108`（空=关闭）；路径 `/metrics`
- RTMP URL：兼容常见 `rtmp://host/app/stream`（末段为 stream key）；亦支持 gortmplib 的 `rtmp://host/app#stream`

## 架构概览

```
module github.com/kevin197011/stream-runner
cmd/stream-runner → internal/runner
  config load → AppState workers → gortmplib play→publish remux (+ PTS pace)
  slog JSON file + daily rotate (keep 30d)
  SIGHUP reload / SIGTERM stop
  HTTP /metrics (METRICS_ADDR, default :9108)
```

采纳：bluenviron/gortmplib `client-republish`（MIT）。放弃：ffmpeg `-c copy` 子进程（去系统依赖、单二进制）。

## 数据模型

`streams.yml`：

```yaml
streams:
  - id: string # unique
    src: string # RTMP URL
    dst: string # RTMP URL
```

## API 摘要

无对外 HTTP API。运维面：systemd + 信号 + 日志文件。

## 部署与运维

| 项     | 值                                                                                |
| ------ | --------------------------------------------------------------------------------- |
| 二进制 | `/usr/local/bin/stream-runner`                                                    |
| 配置   | `/etc/stream-runner/streams.yml`                                                  |
| 模板   | `/usr/share/stream-runner/streams.yml`                                            |
| unit   | `/etc/systemd/system/stream-runner.service`                                       |
| 日志   | `/var/log/stream-runner/stream.log`                                               |
| 打包   | GoReleaser（同 krun）：`v*` tag / workflow_dispatch → Release（tar.gz + deb/rpm） |
| 安装   | 根目录 `deploy.sh`：落包/解包、写配置、enable/restart；`--uninstall` 停服并卸包   |
| unit   | `systemd/stream-runner.service`（随 Release 打进 deb/rpm/tar.gz）                 |

## 安全与已知风险

- 以 root 跑服务；源/目标 URL 来自本地配置文件，需限制写权限
- 日志可能含源主机与 IP；勿把敏感推流密钥写进 id
- 极端厂商 RTMP 方言可能不如 ffmpeg 宽；遇到再补兼容或回退路径

## 测试验收清单

```bash
go test ./...
go build -o /tmp/stream-runner ./cmd/stream-runner
```

回归要点：配置加载（含重复 id）、worker 停后不再重启、PTS 落后告警、日志轮转后可写、URL 末段→stream key 规范化。

## 变更记录

| 版本  | 日期       | 说明                                                                                           |
| ----- | ---------- | ---------------------------------------------------------------------------------------------- |
| 1.1.0 | 2026-11-23 | 原生 gortmplib remux，去掉 ffmpeg 运行时依赖；指标 `relay_restarts_total`                      |
| 1.0.0 | 2025       | 初版多路拉流、热重载、打包                                                                     |
| —     | 2026-11-11 | 代码布局改为 `cmd/` + `internal/runner`；配置与 unit 落盘到仓库                                |
| —     | 2026-11-11 | 日志改为按日轮转，默认保留 30 天                                                               |
| —     | 2026-11-11 | 构建基线升到 Go 1.27（当前稳定版 1.27.4）                                                      |
| —     | 2026-11-11 | 删除 `scripts/`；deb/rpm 仅由 GitHub Release 产出                                              |
| —     | 2026-11-11 | 采用 GoReleaser（对齐 krun）                                                                   |
| —     | 2026-11-11 | `deploy.sh` 一键安装/卸载                                                                      |
| —     | 2026-11-11 | 修复 Release：不再 chmod 已删除的 packaging/；module 路径 github.com/kevin197011/stream-runner |

## 开发进度记录

### 2026-11-23

- **做了什么**：冒烟修 pace：增量 behind、只用视频 PTS、负值归零；加 `STREAM_RUNNER_*` 路径覆盖；MediaMTX `live/src`→`live/dst` 通
- **决策**：路径 env 覆盖便于本机验收，默认仍 `/etc` `/var`
- **验证**：`go test ./...`；ffprobe dst → h264+aac；`stream_up=1`；`source pace` behind≈0 / speed≈1.0x
- **遗留**：无

- **做了什么**：FR 改原生 RTMP remux（gortmplib）；去掉 ffmpeg 检查/Depends/`deploy` 安装；PTS pace 替代 progress 管；指标改名 `stream_runner_relay_restarts_total`
- **决策**：采纳 gortmplib republish；`rtmp://host/app/stream` 末段归一化为 `#stream` 以对齐原 ffmpeg URL；放弃继续壳 ffmpeg（要单二进制、无系统依赖）
- **验证**：`go test ./...`；`go build ./cmd/stream-runner`
- **遗留**：现网奇怪 RTMP 方言若失败再补

### 2026-11-11

- **做了什么**：FR-SR-08 Prometheus 指标：`stream_runner_stream_up` / `stream_runner_ffmpeg_restarts_total` / `stream_runner_behind_seconds` + Go/process；`METRICS_ADDR` 默认 `:9108`，空关闭；删流时 `DeleteLabelValues`
- **决策**：对齐 client_golang 惯例；不另开管理 API；behind 复用 progress 管
- **验证**：`go test ./...`；`curl -s localhost:9108/metrics`（本地起服务时）
- **遗留**：无
