package util

import (
	"time"

	"github.com/M1saka10010/SwallowAgent/model"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// GetMemoryInfo 获取内存信息
func GetMemoryInfo() (uint64, uint64, uint64, uint64, error) {
	// 获取内存信息
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, 0, 0, err
	}

	// 获取交换区信息
	swapStat, err := mem.SwapMemory()
	if err != nil {
		return 0, 0, 0, 0, err
	}

	return vmStat.Total, vmStat.Used, swapStat.Total, swapStat.Used, nil
}

// GetDiskInfo 获取磁盘信息
func GetDiskInfo(path string) (uint64, uint64, error) {
	// 获取磁盘信息
	if path == "" {
		path = "/"
	}
	diskStat, err := disk.Usage(path)
	if err != nil {
		return 0, 0, err
	}

	return diskStat.Total, diskStat.Used, nil
}

// GetCPUInfo 获取CPU使用率
func GetCPUInfo() (float64, error) {
	// 获取CPU使用率
	cpuPercent, err := cpu.Percent(0, false)
	if err != nil {
		return 0, err
	}

	if len(cpuPercent) > 0 {
		return cpuPercent[0], nil
	}
	return 0, nil
}

// GetNetworkInfo 获取指定网络接口的流量信息
func GetNetworkInfo(interfaceName string) (uint64, uint64, error) {
	// 获取网络接口信息
	counters, err := net.IOCounters(true)

	if err != nil {
		return 0, 0, err
	}

	recv, send := uint64(0), uint64(0)

	for _, counter := range counters {
		if interfaceName == "" || interfaceName == counter.Name {
			recv += counter.BytesRecv
			send += counter.BytesSent
		}
	}

	return recv, send, nil

}

// GetUsageInfo 获取系统使用情况
func GetUsageInfo(lastUsage *model.UsageInfo, paths []string, interfaceNames []string) (*model.UsageInfo, error) {
	cpuUsage, err := GetCPUInfo()
	if err != nil {
		return nil, err
	}

	memTotal, memUsed, swapTotal, swapUsed, err := GetMemoryInfo()
	if err != nil {
		return nil, err
	}

	// 获取磁盘信息
	diskTotal, diskUsed := uint64(0), uint64(0)
	for _, path := range paths {
		diskTotal1, diskUsed1, err := GetDiskInfo(path)
		if err != nil {
			return nil, err
		}
		diskTotal += diskTotal1
		diskUsed += diskUsed1
	}
	if len(paths) == 0 {
		diskTotal, diskUsed, err = GetDiskInfo("")
		if err != nil {
			return nil, err
		}
	}

	// 获取网络信息
	netRecv, netSend := uint64(0), uint64(0)
	for _, interfaceName := range interfaceNames {
		netRecv1, netSend1, err := GetNetworkInfo(interfaceName)
		if err != nil {
			return nil, err
		}
		netRecv += netRecv1
		netSend += netSend1
	}
	if len(interfaceNames) == 0 {
		netRecv, netSend, err = GetNetworkInfo("")
		if err != nil {
			return nil, err
		}
	}

	// 获取负载信息
	loadStat, err := load.Avg()
	if err != nil {
		return nil, err
	}

	// 获取当前时间戳
	timeStamp := uint64(time.Now().Unix())

	usageInfo := &model.UsageInfo{
		CPUUsage:    cpuUsage,
		MemoryTotal: memTotal,
		MemoryUsed:  memUsed,
		SwapTotal:   swapTotal,
		SwapUsed:    swapUsed,
		DiskTotal:   diskTotal,
		DiskUsed:    diskUsed,
		NetRecv:     netRecv,
		NetSend:     netSend,
		Load1:       loadStat.Load1,
		Load5:       loadStat.Load5,
		Load15:      loadStat.Load15,
		TimeStamp:   timeStamp,
	}

	if lastUsage != nil && timeStamp > lastUsage.TimeStamp {
		elapsed := float64(timeStamp - lastUsage.TimeStamp)
		// 计数器重置或回绕时差值为负，置 0 避免 uint64 下溢得到巨大值
		if netRecv >= lastUsage.NetRecv {
			usageInfo.NetRecvSpeed = float64(netRecv-lastUsage.NetRecv) / elapsed
		}
		if netSend >= lastUsage.NetSend {
			usageInfo.NetSendSpeed = float64(netSend-lastUsage.NetSend) / elapsed
		}
	}

	return usageInfo, nil

}
