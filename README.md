# SwallowAgent

一个用 Go 编写的轻量级服务器监控 Agent。它周期性采集主机的系统信息与资源使用情况（CPU、内存、磁盘、网络、负载），通过 **WebSocket** 长连接上报到指定服务端。

## 特性

- 跨平台采集（基于 [gopsutil](https://github.com/shirou/gopsutil)）
- 基于 WebSocket 单条长连接上报，支持自动重连（指数退避 1s → 30s）
- 系统信息与使用情况分别采集，使用统一报文封装区分
- 支持多磁盘路径、多网卡聚合统计
- 网络收发速率实时计算
- 通过 Ed25519 公钥注册与挑战签名鉴权（不兼容旧 Token）
- 支持 `SIGINT` / `SIGTERM` 优雅退出

## 安装与构建

需要 Go 1.25 及以上版本。

```bash
git clone https://github.com/M1saka10010/SwallowAgent.git
cd SwallowAgent
go build -o swallow-agent .
```

### 固定 Release 一键交付（Linux amd64 / arm64）

面板必须由管理员配置明确的 `AGENT_VERSION`（带 v 前缀的严格 SemVer，可带 `-预发布标识` 和 `+构建元数据`），未配置时禁用 release 命令；不得默认 `latest` 或将旧 Token 版本 v0.0.2 当作目标。

先设置 `AGENT_VERSION` 为已审核且实际发布的 tag，并运行 `sudo -v` 完成权限认证。下面一条命令交互读取一次性令牌（不回显），下载**同一固定 tag** 的 bootstrap，再由它下载对应架构 binary、install.sh 和 checksums.txt，校验后安装：

```bash
(set -euo pipefail; : "${AGENT_VERSION:?请先设置已审核且实际发布的目标 tag}"; [[ "$AGENT_VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][A-Za-z0-9-]*))*)?(\+[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*)?$ ]]; read -rsp '一次性注册令牌: ' TOKEN; printf '\n' >&2; SCRIPT=$(curl --disable --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 15 --max-time 300 "https://github.com/M1saka10010/SwallowAgent/releases/download/$AGENT_VERSION/bootstrap.sh"); printf %s "$TOKEN" | sudo -n bash -c "$SCRIPT" -- --version "$AGENT_VERSION" --mode install --report-url wss://example.com/report --enrollment-token-stdin)
```

面板可将同一模板中的版本、地址和模式替换为经过校验且正确 shell quoting 的值，凭据只能通过 stdin；脚本代码通过 `bash -c` 参数传入，**不能用 `curl | bash` 与凭据争用 stdin**。也可先审核下载的 bootstrap，再执行 `printf %s "$TOKEN" | sudo bash bootstrap.sh --version "$AGENT_VERSION" --mode install --report-url wss://example.com/report --enrollment-token-stdin`。不要开启 `set -x` 或记录凭据。

bootstrap 仅允许 HTTPS（包括重定向），拒绝未知系统/架构；任一下载失败、缺失/重复摘要或 SHA-256 不符均不执行资产。下载前先在 `${TMPDIR:-/tmp}` 创建 0700 私有临时目录，并直接执行探测脚本（不读取凭据 stdin）；不可写或 `noexec` 时依次尝试 `/var/tmp`、`/opt`。全部失败则明确报错且不下载。不会修改挂载选项、已有目录权限或预先创建 Agent 安装目录；只清理本次 mktemp 创建的目录。正常/失败/可捕获信号退出会清理；子脚本退出码原样返回。INT/TERM/HUP 转交子进程 TERM 以触发子脚本回滚与清理，入口分别退出 130/143/129；SIGKILL/断电无法清理。`--binary` 本地高级模式保留在下述两个原始脚本中，不经 bootstrap 下载。

**信任边界：** 上述初次 bootstrap 依赖 HTTPS、GitHub 仓库/release 管理者和本机 CA 信任；它不能证明自己未被篡改。同一 release 的 checksums.txt 只校验下载资产与该清单一致，**不是独立签名**，不能抵御发布账户被攻陷或资产与清单同时替换；固定 tag 也不保证资产永不可变。更严格环境应先从独立可信渠道核验 bootstrap/资产摘要或审核源码后使用本地 `--binary`，不要把在线同源摘要描述为供应链签名验证。

维护者可在仓库根目录运行 `bash tests/bootstrap_test.sh`。Linux 上还可用以下命令验证真实 `noexec` 自动回退（需要 sudo、unshare 和 mount 权限；仓库须位于 `/tmp` 之外）。挂载仅存在于私有 mount namespace，不修改宿主 `/tmp`；测试使用 mock 下载和安装器，不联网、不修改已有 Agent 安装：

```bash
sudo unshare --mount --propagation private bash -c 'mount -t tmpfs -o noexec,nosuid,nodev tmpfs /tmp && bash tests/bootstrap_noexec_test.sh'
```

发布维护者须先创建指向审核 commit 的 tag，再手动运行 `.github/workflows/release.yml` 并传入该已有 tag。流程解析 tag commit，Linux/Windows 原生 `go test ./...` 和 `go vet ./...` 门禁、Linux shell 语法及 mock 下载测试通过后才构建；Linux 另以 root 运行安装/升级 mock 测试避免 skip。所有构建与脚本来自同一 commit，发布前复核 tag 未移动，并显式设置 `target_commitish`。资产包括 Linux amd64/arm64、Windows amd64 binary、三个脚本和 checksums.txt；此改动本身不创建 tag 或发布。

### 常规公钥版本升级（无需注册令牌）

审核并下载同一目标 release 的 bootstrap 后执行：

```bash
sudo bash bootstrap.sh --version "$AGENT_VERSION" --mode upgrade
# 可选：追加 --github-proxy "$GITHUB_PROXY"
```

契约为 `--version TAG --mode upgrade [--github-proxy PREFIX]`，拒绝 token、report-url、dev-mode 参数。Monitor 必须检查目标 release 中 **upgrade.sh 资产存在** 才启用常规升级能力，不能仅看版本字符串或 bootstrap.sh 是否存在。旧 release 的 bootstrap 不支持代理参数；安装代理选项也必须以该新能力门禁保护，不应向旧脚本传入新参数。发布资产现为三个脚本（bootstrap/install/upgrade）、三个 binary 和 checksums.txt。

`--github-proxy` 为 HTTPS URL 前缀（主机、可选端口和简单路径；禁止凭据、查询、片段、空白、点段），下载 URL 为 `PREFIX/原始完整GitHub资产URL`。适用于新 bootstrap 的三种模式，覆盖 checksums、binary、模式脚本全部下载；初次 bootstrap 下载由命令生成方使用相同前缀，bootstrap 不能代理它自身此前的下载。仅信任经过审核的代理，它可同时修改清单与资产；SHA256 不是独立签名。

仅支持 Linux systemd amd64/arm64 和 `/opt/swallow-agent` 下由 install 创建的 0700 布局，或完成公钥迁移后的 0755 布局。精确匹配 unit、迁移服务链接、布局标记和默认私钥路径；拒绝 drop-in、待 reload 单元、未知 fragment、不安全所有权/权限、硬链接/非标准符号链接。路径检查是保守格式检查，不是完整 YAML schema 验证。共用 `.install.lock`；下载、SHA256 和 15 秒有界候选 `-version` 校验全部发生在停服之前。当前版本也必须能返回严格 SemVer，拒绝 `dev`、未知版本和 SemVer 降级；允许同版本重装，构建元数据不参与排序。

只原子替换 binary，保留 config、agent.key、unit、链接和 enable 状态。原 active 服务启动后检查两次；原 inactive 服务保持停止，不启动候选验证。active **不代表认证、上报或入库成功**，应在 Monitor 确认版本和新数据。失败或可捕获信号尝试恢复原 binary 与运行状态，失败会显式告警；成功和停服后失败均保留 0700 `.upgrade.*` 中原 binary，确认稳定后人工清理。SIGKILL/断电不能自动回滚。升级后 Token 迁移完成标记的旧 binary hash 不再匹配，完成过迁移的主机**不要再重跑旧版迁移脚本**；升级也不会修改原 key/config。

高级本地模式：`sudo bash upgrade.sh --binary PATH --version TAG --sha256 TRUSTED_SHA256`，摘要必须来自可信渠道。测试门禁包含 `go test -run TestUpgrade -count=1 .`（root 下运行隔离路径和 mock systemctl，绝不操作真实服务）、`bash tests/bootstrap_test.sh` 及全量 Go 测试。

### Linux 安装（本地构建版本）

先执行上述构建，再安装可信的本地二进制；脚本不下载 release，避免安装旧 Token 版本。

```bash
printf %s '<一次性注册令牌>' | sudo bash install.sh --binary ./swallow-agent --report-url wss://example.com/report --enrollment-token-stdin
```

在 Monitor 管理界面创建设备并获取注册命令。令牌为 64 位小写 hex，有效期 15 分钟且只能使用一次。脚本创建 `/opt/swallow-agent`（0700）、二进制、`agent.key`（0600）、`config.yml`（0600）和 systemd 服务文件，注册成功后自动执行以下命令，**无需批准或额外启用步骤**：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now swallow-agent.service
```

脚本用不含令牌的状态标记识别本次安装。注册、写配置或启动失败后可使用相同地址和模式重跑，始终保留并复用原私钥。非本脚本管理的既有安装、符号链接或不同服务文件拒绝覆盖；本脚本已有配置和二进制不会被覆盖。开发环境仅可追加 `--dev-mode` 使用 loopback `ws://`，不会跳过 TLS 验证。

令牌是短期 bearer 凭据：持有者可抢先绑定自己的公钥，签名只证明私钥持有，不能防止被窃令牌抢注。请通过可信渠道传递，避免聊天、日志、shell 历史和录屏泄露；不要开启 shell tracing。Agent 不把令牌写入配置或日志。疑似泄露应重新签发令牌；若已被绑定，应先核查设备与公钥，不能把重试当作抢注修复。

### 从 v0.0.2 Token 旧版本升级

自 v0.1.2 起移除 Token 到公钥的迁移渠道（迁移脚本、bootstrap `--mode migrate-token-to-key` 与 Monitor 界面命令）。仍在 v0.0.2 的设备请 git 检出过渡版本 v0.1.1（`git clone` 本仓库后 `git checkout v0.1.1`），按该版本的 README 与 release 资产完成迁移，之后即可使用常规升级；已公钥化的设备不受影响。

查看运行状态与日志：

```bash
sudo systemctl status swallow-agent.service
sudo journalctl -u swallow-agent.service -f
```


## 运行

查看版本：`./swallow-agent -version`（Windows：`.\swallow-agent.exe -version`）。只打印版本并退出，无需配置、私钥或网络，不能与其他参数组合。普通源码构建默认为 `dev`；release 所有平台通过 `-ldflags -X github.com/M1saka10010/SwallowAgent/buildinfo.Version=<tag>` 注入校验后的实际 tag，与 `system_info.data.agentVersion` 使用同一版本来源。

手动自动注册（将令牌通过标准输入传入）：

```bash
printf %s '<一次性注册令牌>' | ./swallow-agent -enroll wss://example.com/report -private-key ./agent.key -enrollment-token-stdin
```

私钥不存在则独占生成，存在则复用，失败和重试不会换钥。先用现有 key 完成一次有 10 秒上限的正常 report 认证；成功则跳过注册。注册响应丢失或失败后再做一次相同认证来恢复。此恢复只证明 key 已在同服务绑定，不证明它对应输入令牌的目标设备；只应用于重跑同一安装。注册 POST 到同源 HTTPS `/enroll`，签名内容为 `swallow-enroll-v1\n<token>\n<publicKey>`，返回 `publicId`；拒绝所有 HTTP 重定向，HTTP 请求超时 10 秒，响应上限 4096 字节。完整网络流程最多三次各 10 秒操作，不无限重连。开发可追加 `-dev-mode`，仅允许 loopback 明文 WS/HTTP。

手动公钥部署仍可先生成每台设备独立的密钥，将输出的公钥注册到 Monitor，然后设置配置中的 `privateKeyPath`：

```bash
./swallow-agent -generate-key ./agent.key
./swallow-agent -show-public-key ./agent.key
```

私钥为单个 PKCS8 PEM Ed25519 私钥。POSIX 下必须是权限严格为 0600 的普通文件，拒绝符号链接；Windows 不设置或校验 ACL、所有者，使用系统默认文件权限，仅检查普通文件并拒绝末级重解析点。生成命令独占创建，不覆盖已有路径。请将私钥置于可信目录，勿上传或共享私钥。当前 Monitor 不支持原地换钥：轮换需生成新文件、删除旧设备并使用新公钥重建设备；删除设备也会删除历史数据，操作前请确认影响。

POSIX 手动运行：

```bash
./swallow-agent -c config.yaml
```

### Windows 手动运行

Windows 支持密钥生成、自动注册和前台采集上报；`install.sh` 与 `upgrade.sh` 仅用于 Linux/systemd，不安装 Windows 服务。运行账户需要有私钥读取权限；Agent 不要求文件所有者与运行账户相同，也不会修改已有文件权限。

在可信目录内放置 `swallow-agent.exe`，从 Monitor 原设备或新设备取得一次性注册凭据，通过 PowerShell 执行（凭据属于敏感信息，避免保存到命令历史）：

```powershell
'<一次性注册令牌>' | .\swallow-agent.exe -enroll 'wss://example.com/report' -private-key '.\agent.key' -enrollment-token-stdin
```

创建 `config.yaml`，将磁盘和私钥路径改为 Windows 路径；推荐私钥使用绝对路径。YAML 的单引号字符串可直接保留反斜杠：

```yaml
paths:
  - 'C:\'
interfaceNames: []
interval: 3
reportUrl: wss://example.com/report
privateKeyPath: 'C:\SwallowAgent\agent.key'
devMode: false
isDebug: false
```

将示例中的私钥路径改为刚才生成文件的实际位置，然后运行：

```powershell
.\swallow-agent.exe -c .\config.yaml
```

已有公钥绑定时可直接加载原私钥运行；不要重生成私钥来替代配置或权限修复。Windows 私钥的保密性由系统及目录权限负责：即使其他账户可读，Agent 也不会拒绝加载；能够复制私钥的人可以冒充该设备。请勿放入共享目录。文件级检查不保证祖先目录没有 junction。

若生成密钥遇到文件检查、写入或关闭失败，可能留下不完整文件；为避免误删或替换已有身份，程序不会自动覆盖它。请先检查并备份该文件，确认没有已绑定的有效私钥后再处理，不要直接删除重试。

| 参数 | 说明 |
| ---- | ---- |
| `-version` | 单独打印 Agent 版本并退出，无需配置 |
| `-c` | 指定 YAML 配置文件路径（采集上报时必填） |
| `-enroll` | 自动注册的 report URL；与 `-private-key` 和 `-enrollment-token-stdin` 同时使用 |
| `-private-key` | 自动注册使用的私钥文件，存在则复用 |
| `-enrollment-token-stdin` | 从标准输入读取一次性令牌，不提供令牌命令行参数 |
| `-dev-mode` | 自动注册时允许 loopback WS/HTTP |
| `-generate-key` | 独占生成指定私钥文件，只打印 base64 公钥；不加载配置 |
| `-show-public-key` | 从指定私钥文件读取并打印 base64 公钥；不加载配置 |

两个密钥命令互斥，不能与 `-c` 组合。旧 `token` 字段及未知字段会被拒绝，配置必须只有一个 YAML 文档。

## 配置

`config.yaml` 示例：

```yaml
# 磁盘目录（为空则采集根目录 /）
paths:
  - /
# 网卡名称（为空则聚合所有网卡）
interfaceNames:
# 使用情况采集间隔（秒）；系统信息按此间隔的 20 倍上报
interval: 3
# 生产环境必须使用 wss://
reportUrl: wss://your-server.example.com/report
privateKeyPath: /opt/swallow-agent/agent.key
devMode: false
# 调试模式
isDebug: true
```

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| `paths` | `[]string` | 需要统计的磁盘挂载点/盘符；为空时默认统计根目录 `/`。多个路径会聚合求和 |
| `interfaceNames` | `[]string` | 需要统计的网卡名称；为空时聚合所有网卡 |
| `interval` | `uint64` | 使用情况采集间隔（秒），必须大于 0。系统信息按该间隔的 **20 倍** 上报 |
| `reportUrl` | `string` | 生产使用 `wss://`，路径必须为 `/report`；禁止 userinfo、查询参数和 fragment |
| `privateKeyPath` | `string` | 本地 Ed25519 私钥路径，建议使用绝对路径 |
| `devMode` | `bool` | 仅允许 localhost 或 loopback IP 的明文 `ws://`，默认 false，不跳过 TLS 证书校验 |
| `isDebug` | `bool` | 是否打印上报调试日志 |

## 通信协议

### 连接

- 生产协议：WSS；开发环境可显式开启 `devMode` 使用 loopback WS
- 握手请求头：`X-Agent-Public-Key: <标准 base64 Ed25519 公钥>`，不发送 Token
- 服务端发送 `{"type":"auth_challenge","challenge":"<32 字节随机数的标准 base64>"}`
- 客户端签名 UTF-8 字节串 `swallow-report-auth-v1\n<公钥 base64>\n<challenge base64>`（没有末尾换行），发送 `{"type":"auth_response","signature":"<签名 base64>"}`
- 必须收到 `{"type":"auth_ok"}` 后才开始上报；连接与认证共享 10 秒预算，入站消息上限 4096 字节。拒绝无效挑战、认证拒绝和超大消息
- 客户端使用单条长连接发送所有报文；连接断开后自动重连（退避 1s → 30s）

### 报文格式

所有报文均为 JSON 文本帧，采用统一封装结构：

```json
{
  "type": "<报文类型>",
  "data": { /* 与类型对应的数据对象 */ }
}
```

| `type` | 说明 | 上报频率 |
| ------ | ---- | -------- |
| `system_info` | 系统静态信息 | 每 `interval × 20` 秒 |
| `system_usage` | 系统资源使用情况 | 每 `interval` 秒 |

#### `system_info` 数据字段

```json
{
  "type": "system_info",
  "data": {
    "hostname": "my-server",
    "agentVersion": "dev",
    "uptime": 123456,
    "bootTime": 1700000000,
    "modelName": "Intel(R) Core(TM) i7-9700K CPU @ 3.60GHz",
    "cores": 8,
    "processes": 312,
    "os": "linux",
    "platform": "ubuntu",
    "platformFamily": "debian",
    "platformVersion": "22.04",
    "kernelVersion": "5.15.0-1.el8",
    "kernelArch": "x86_64",
    "virtualizationSystem": "kvm",
    "virtualizationRole": "guest",
    "hostId": "550e8400-e29b-41d4-a716-446655440000"
  }
}
```

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| `hostname` | `string` | 主机名 |
| `agentVersion` | `string` | Agent 构建版本；源码默认 `dev`，release 为实际 tag，与 `-version` 一致 |
| `uptime` | `uint64` | 系统运行时间（秒） |
| `bootTime` | `uint64` | 系统启动时间（Unix 时间戳，秒） |
| `modelName` | `string` | CPU 型号名称 |
| `cores` | `int32` | CPU 核心数 |
| `processes` | `uint64` | 当前进程数 |
| `os` | `string` | 操作系统类型（如 `linux`、`freebsd`） |
| `platform` | `string` | 平台名称（如 `ubuntu`、`centos`） |
| `platformFamily` | `string` | 平台家族（如 `debian`、`rhel`） |
| `platformVersion` | `string` | 平台版本 |
| `kernelVersion` | `string` | 内核版本 |
| `kernelArch` | `string` | 内核架构（如 `x86_64`） |
| `virtualizationSystem` | `string` | 虚拟化系统（如 `kvm`、`xen`） |
| `virtualizationRole` | `string` | 虚拟化角色（`guest` / `host`） |
| `hostId` | `string` | 主机唯一标识（UUID） |

#### `system_usage` 数据字段

```json
{
  "type": "system_usage",
  "data": {
    "cpuUsage": 23.5,
    "memoryTotal": 16777216000,
    "memoryUsed": 8388608000,
    "swapTotal": 2147483648,
    "swapUsed": 0,
    "diskTotal": 512110190592,
    "diskUsed": 256055095296,
    "netRecv": 1048576000,
    "netSend": 524288000,
    "netRecvSpeed": 10240.5,
    "netSendSpeed": 5120.25,
    "load1": 0.52,
    "load5": 0.48,
    "load15": 0.40,
    "timestamp": 1700000123
  }
}
```

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| `cpuUsage` | `float64` | CPU 使用率（百分比，0–100） |
| `memoryTotal` | `uint64` | 内存总量（字节） |
| `memoryUsed` | `uint64` | 内存使用量（字节） |
| `swapTotal` | `uint64` | 交换区总量（字节） |
| `swapUsed` | `uint64` | 交换区使用量（字节） |
| `diskTotal` | `uint64` | 磁盘总量（字节，多路径聚合） |
| `diskUsed` | `uint64` | 磁盘使用量（字节，多路径聚合） |
| `netRecv` | `uint64` | 网络累计接收量（字节） |
| `netSend` | `uint64` | 网络累计发送量（字节） |
| `netRecvSpeed` | `float64` | 网络接收速率（字节/秒）；首次上报为 0 |
| `netSendSpeed` | `float64` | 网络发送速率（字节/秒）；首次上报为 0 |
| `load1` | `float64` | 1 分钟平均负载 |
| `load5` | `float64` | 5 分钟平均负载 |
| `load15` | `float64` | 15 分钟平均负载 |
| `timestamp` | `uint64` | 采集时间（Unix 时间戳，秒） |

> 说明：`netRecvSpeed` / `netSendSpeed` 通过相邻两次采集的累计字节差除以时间差计算。首次采集（无历史基准）时为 `0`；当检测到计数器重置或回绕（差值为负）时也会置 `0`，以避免数值溢出。

## 项目结构

```
SwallowAgent/
├── main.go              # 程序入口：加载配置、校验、启动采集协程、信号处理
├── config.yaml.example  # 配置文件示例
├── model/               # 数据模型
│   ├── configModel.go   # 配置结构
│   ├── sysInfoModel.go  # 系统信息结构
│   └── usageModel.go    # 使用情况结构
├── service/
│   └── reportService.go # 采集与上报循环
└── util/
    ├── ws.go            # WebSocket 客户端（自动重连、并发安全写）
    ├── sysInfo.go       # 系统信息采集
    └── usage.go         # 使用情况采集与网速计算
```

## 许可

请根据需要补充许可证信息。
