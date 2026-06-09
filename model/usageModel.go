package model

type UsageInfo struct {
	CPUUsage     float64 `json:"cpuUsage"`     // CPU使用率
	MemoryTotal  uint64  `json:"memoryTotal"`  // 内存总量
	MemoryUsed   uint64  `json:"memoryUsed"`   // 内存使用量
	SwapTotal    uint64  `json:"swapTotal"`    // 交换区总量
	SwapUsed     uint64  `json:"swapUsed"`     // 交换区使用量
	DiskTotal    uint64  `json:"diskTotal"`    // 磁盘总量
	DiskUsed     uint64  `json:"diskUsed"`     // 磁盘使用量
	NetRecv      uint64  `json:"netRecv"`      // 网络接收量
	NetSend      uint64  `json:"netSend"`      // 网络发送量
	NetRecvSpeed float64 `json:"netRecvSpeed"` // 网络接收速度
	NetSendSpeed float64 `json:"netSendSpeed"` // 网络发送速度
	Load1        float64 `json:"load1"`        // 1分钟负载
	Load5        float64 `json:"load5"`        // 5分钟负载
	Load15       float64 `json:"load15"`       // 15分钟负载
	TimeStamp    uint64  `json:"timestamp"`    // 时间戳
}
