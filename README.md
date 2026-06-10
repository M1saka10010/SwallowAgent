# SwallowAgent

一个用 Go 编写的轻量级服务器监控 Agent。它周期性采集主机的系统信息与资源使用情况（CPU、内存、磁盘、网络、负载），通过 **WebSocket** 长连接上报到指定服务端。

## 特性

- 跨平台采集（基于 [gopsutil](https://github.com/shirou/gopsutil)）
- 基于 WebSocket 单条长连接上报，支持自动重连（指数退避 1s → 30s）
- 系统信息与使用情况分别采集，使用统一报文封装区分
- 支持多磁盘路径、多网卡聚合统计
- 网络收发速率实时计算
- 通过 `Token` 请求头进行连接鉴权
- 支持 `SIGINT` / `SIGTERM` 优雅退出

## 安装与构建

需要 Go 1.24 及以上版本。

```bash
git clone https://github.com/M1saka10010/SwallowAgent.git
cd SwallowAgent
go build -o swallow-agent .
```

### Linux 一键安装（管道）

从 [latest release](https://github.com/M1saka10010/SwallowAgent/releases/latest) 下载预编译二进制并自动安装为 systemd 服务。回报地址与 Token 通过参数传入：

```bash
curl -fsSL https://raw.githubusercontent.com/M1saka10010/SwallowAgent/main/install.sh | bash -s -- <reportUrl> <token>
```

示例：

```bash
curl -fsSL https://raw.githubusercontent.com/M1saka10010/SwallowAgent/main/install.sh | bash -s -- wss://mock.594144.xyz/report 1234567890abcdef
```

脚本会：

- 自动识别架构（amd64 / arm64）并下载对应二进制到 `/opt/swallow-agent/swallow-agent`
- 在 `/usr/local/bin/swallow-agent` 创建软链接
- 生成 `/opt/swallow-agent/config.yml` 与 `swallow-agent.service` 并注册到 systemd
- 自动启用并启动服务（开机自启）

查看运行状态与日志：

```bash
sudo systemctl status swallow-agent.service
sudo journalctl -u swallow-agent.service -f
```


## 运行

```bash
./swallow-agent -c config.yaml
```

| 参数 | 说明 |
| ---- | ---- |
| `-c` | 指定 YAML 配置文件路径（必填） |

## 配置

`config.yaml` 示例：

```yaml
# 磁盘目录（为空则采集根目录 /）
paths:
  - C:/
  - D:/
# 网卡名称（为空则聚合所有网卡）
interfaceNames:
# 使用情况采集间隔（秒）；系统信息按此间隔的 20 倍上报
interval: 3
# 回报地址，必须使用 ws:// 或 wss://
reportUrl: wss://mock.594144.xyz/report
# 鉴权 Token
token: 1234567890abcdef
# 调试模式
isDebug: true
```

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| `paths` | `[]string` | 需要统计的磁盘挂载点/盘符；为空时默认统计根目录 `/`。多个路径会聚合求和 |
| `interfaceNames` | `[]string` | 需要统计的网卡名称；为空时聚合所有网卡 |
| `interval` | `uint64` | 使用情况采集间隔（秒），必须大于 0。系统信息按该间隔的 **20 倍** 上报 |
| `reportUrl` | `string` | 上报地址，必须以 `ws://` 或 `wss://` 开头 |
| `token` | `string` | 鉴权 Token，连接时通过请求头 `Token` 携带，不能为空 |
| `isDebug` | `bool` | 是否打印上报调试日志 |

## 通信协议

### 连接

- 协议：WebSocket（`ws://` 或 `wss://`）
- 鉴权：建立连接时在 HTTP 握手请求头中携带 `Token: <token>`
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
├── config.yaml          # 配置文件示例
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
