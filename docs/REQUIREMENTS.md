# Stream Runner 需求文档

## 背景与目标

用 ffmpeg 把多路 RTMP 源流拷贝到目标地址，进程退出后自动重拉；运维侧支持 systemd、SIGHUP 热重载与可轮转的 JSON 日志。

### 非目标

- 不做转码 / 协议转换（仅 `-c copy`）
- 不提供 HTTP 管理 API 或 Web UI
- 不内置 RTMP 服务端

## 用户与权限

- 典型以 root + systemd 运行（写 `/var/run`、`/var/log/stream-runner`、读 `/etc/stream-runner`）
- 无多用户鉴权面

## 功能需求

| 编号     | 需求         | 验收标准                                                                                     |
| -------- | ------------ | -------------------------------------------------------------------------------------------- |
| FR-SR-01 | 多路流并发   | `streams.yml` 中多条 `id/src/dst` 各自独立 ffmpeg                                            |
| FR-SR-02 | 自动重连     | ffmpeg 退出后重拉；短跑失败退避 1s→30s；连续播放 ≥20s 后再断则 1s 重拉                       |
| FR-SR-03 | 配置热重载   | `SIGHUP` / `systemctl reload`：停删改、启新增、换 src/dst；坏配置不改现网 worker             |
| FR-SR-04 | 日志         | 服务 JSON → `stream.log`；ffmpeg 警告/错误带时间戳与 stream id 打到 stdout/stderr            |
| FR-SR-05 | 日志轮转     | 按本地日历日将 `stream.log` 归档为 `stream.log.YYYY-MM-DD`；默认保留 30 天；启动与每小时检查 |
| FR-SR-06 | 源节奏观测   | 进度管解析 `behind_s` / `speed`；慢握手或落后打 `source lagging` / `pull network`            |
| FR-SR-07 | systemd 打包 | deb/rpm 安装二进制、配置模板、unit；首次安装生成 `/etc` 配置且不覆盖已有                     |

## 非功能需求

- Go 1.27+；运行时依赖 ffmpeg
- 配置路径默认 `/etc/stream-runner/streams.yml`
- PID：`/var/run/stream-runner.pid`；ffmpeg 独立进程组便于 Kill

## 架构概览

```
module github.com/kevin197011/stream-runner
cmd/stream-runner → internal/runner
  config load → AppState workers → ffmpeg copy (+ progress probe)
  slog JSON file + daily rotate (keep 30d)
  SIGHUP reload / SIGTERM stop
```

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

| 项     | 值                                                                                         |
| ------ | ------------------------------------------------------------------------------------------ |
| 二进制 | `/usr/local/bin/stream-runner`                                                             |
| 配置   | `/etc/stream-runner/streams.yml`                                                           |
| 模板   | `/usr/share/stream-runner/streams.yml`                                                     |
| unit   | `/etc/systemd/system/stream-runner.service`                                                |
| 日志   | `/var/log/stream-runner/stream.log`                                                        |
| 打包   | GoReleaser（同 krun）：`v*` tag / workflow_dispatch → Release（tar.gz + deb/rpm）          |
| 安装   | 根目录 `deploy.sh`：装 ffmpeg、落包/解包、写配置、enable/restart；`--uninstall` 停服并卸包 |
| unit   | `systemd/stream-runner.service`（随 Release 打进 deb/rpm/tar.gz）                          |

## 安全与已知风险

- 以 root 跑服务；源/目标 URL 来自本地配置文件，需限制写权限
- 日志可能含源主机与 IP；勿把敏感推流密钥写进 id

## 测试验收清单

```bash
go test ./...
go build -o /tmp/stream-runner ./cmd/stream-runner
```

回归要点：配置加载（含重复 id）、worker 停后不再重启、进度落后告警、日志轮转后可写。

## 变更记录

| 版本  | 日期       | 说明                                                                                           |
| ----- | ---------- | ---------------------------------------------------------------------------------------------- |
| 1.0.0 | 2025       | 初版多路拉流、热重载、打包                                                                     |
| —     | 2026-11-11 | 代码布局改为 `cmd/` + `internal/runner`；配置与 unit 落盘到仓库                                |
| —     | 2026-11-11 | 日志改为按日轮转，默认保留 30 天                                                               |
| —     | 2026-11-11 | 构建基线升到 Go 1.27（当前稳定版 1.27.4）                                                      |
| —     | 2026-11-11 | 删除 `scripts/`；deb/rpm 仅由 GitHub Release 产出                                              |
| —     | 2026-11-11 | 采用 GoReleaser（对齐 krun）                                                                   |
| —     | 2026-11-11 | `deploy.sh` 一键安装/卸载                                                                      |
| —     | 2026-11-11 | 修复 Release：不再 chmod 已删除的 packaging/；module 路径 github.com/kevin197011/stream-runner |

## 开发进度记录

### 2026-11-11

- **做了什么**：Release workflow 去掉已删除的 `packaging/*.sh` chmod；改为 `chmod +x deploy.sh`（对齐 krun）；`.golangci.yml` goimports local-prefixes 跟上 `github.com/kevin197011/stream-runner`
- **决策**：包 hooks 已并入 `deploy.sh`，CI 不再依赖 `packaging/`
- **验证**：workflow 无 `packaging` 引用；`bash -n deploy.sh`；`go test ./...`
- **遗留**：无

### 2026-11-11

- **做了什么**：自动打 tag 收进 `kk-git`（`git:auto_commit_push_tag`）；本仓 `Rakefile` 只 invoke 该 task
- **决策**：产品仓共用库能力，不自维护 semver 逻辑
- **验证**：本地安装 kk-git 0.2.9 后 `rake -T` 含 `git:auto_commit_push_tag`
- **遗留**：无

- **做了什么**：删 `packaging/` hooks；逻辑并入 `deploy.sh`（`setup_service` / uninstall）；unit 迁到 `systemd/stream-runner.service`；GoReleaser 不再挂 nfpm scripts
- **决策**：安装编排只认 `deploy.sh`，包只带文件与 Depends
- **验证**：`bash -n deploy.sh`；`goreleaser check`
- **遗留**：无

- **做了什么**：`deploy.sh` 缺 ffmpeg 时用 apt/dnf/yum 自动安装；deb 优先 `apt-get install`（解析 Depends）
- **决策**：运行时硬依赖只有 ffmpeg，装服务前先保证在 PATH
- **验证**：`bash -n deploy.sh`
- **遗留**：无

- **做了什么**：新增根目录 `deploy.sh`（对齐 krun 用法）：`curl \| sudo bash` 安装/升级，`--uninstall` 卸载；优先 GitHub Release deb/rpm
- **决策**：仅 Linux；配置文件卸载时保留；无包管理器时用 tar.gz 落二进制 + unit
- **验证**：`bash -n deploy.sh`；`./deploy.sh --help`
- **遗留**：无

- **做了什么**：`Rakefile` 改为调用 `kk-git`（`git:auto_commit_push`），删掉自写 commit/push；加 `Gemfile`
- **决策**：与 kkartifact / krun 同一套，不维护第二份智能 commit
- **验证**：`bundle install`；`rake -T` 含 `push` / `git:auto_commit_push`
- **遗留**：无

- **做了什么**：按 krun 换成 GoReleaser：新增 `.goreleaser.yaml` + 精简 `release.yml`；删除 `nfpm.yaml`；补 `LICENSE`
- **决策**：linux/amd64+arm64；deb/rpm 依赖 ffmpeg，装 unit + 配置模板；不再手写 nfpm/CI 编包步骤
- **验证**：`go test ./...`；`goreleaser check`（若本机有）
- **遗留**：无

- **做了什么**：删除整个 `scripts/`（含 `deploy.rb`）；nfpm hooks 迁到 `packaging/`；打包只走 GitHub Release workflow
- **决策**：本地不再维护第二套编包入口
- **验证**：确认无 `scripts/` 引用；`go test ./...`
- **遗留**：无

- **做了什么**：清理无用物：删 `.rubocop.yml`、无 compose 的 `rake run/docker`、未使用的 `rtmpAddr`、空转测试/Benchmark、nfpm 空字段；README 去掉已不存在的 `deploy.sh` 流程；示例配置缩到 2 路
- **决策**：保留 `rake push` / `deploy`；lint 去掉 `dupl`/`gocritic`
- **验证**：`go test ./...`；`go build ./cmd/stream-runner`
- **遗留**：无

- **做了什么**：`go.mod` / CI / 文档改为 Go 1.27（当前稳定 `go1.27.4`）
- **决策**：跟官方 stable 主线，不钉 patch
- **验证**：`go test ./...`；`go build ./cmd/stream-runner`
- **遗留**：无

- **做了什么**：日志从按大小（100MB / 5 份）改为按本地日历日归档 `stream.log.YYYY-MM-DD`，`LogKeepDays=30`；清理过期按日文件与旧 `.N` 编号文件
- **决策**：用文件 ModTime 的本地日判断跨日（无第三方 lumberjack）；小时级检查足够跨午夜
- **验证**：`go test ./internal/runner/ -run 'Log'`；`go test ./...`
- **遗留**：无

- **做了什么**：根目录单体拆为 `cmd/stream-runner` + `internal/runner`；`config/streams.yml`、`packaging/stream-runner.service` 作为打包同源；deploy/CI 改为拷贝并 `go build ./cmd/stream-runner`
- **决策**：只保留一个 `internal/runner` 包，不按域再拆子包（单二进制、无第二调用方）
- **验证**：`go test ./...` 通过；`go build -o /tmp/stream-runner ./cmd/stream-runner` 通过
- **遗留**：无
