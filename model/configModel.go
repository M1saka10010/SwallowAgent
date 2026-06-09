package model

type Config struct {
	// 磁盘目录
	Paths []string `yaml:"paths"`
	// 网卡名称
	InterfaceNames []string `yaml:"interfaceNames"`
	// 采集间隔
	Interval uint64 `yaml:"interval"`
	// 回报地址
	ReportURL string `yaml:"reportUrl"`
	// Token
	Token string `yaml:"token"`
	// 调试模式
	IsDebug bool `yaml:"isDebug"`
}
