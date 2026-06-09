package util

import (
	"github.com/M1saka10010/SwallowAgent/model"

	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
)

func GetSystemInfo() (*model.SysInfo, error) {
	// 获取主机信息
	hostInfo, err := host.Info()
	if err != nil {
		return nil, err
	}

	// 获取CPU信息
	cpuInfos, err := cpu.Info()
	if err != nil {
		return nil, err
	}

	// 提取CPU型号
	cpuModel := "Unknown"
	if len(cpuInfos) > 0 && cpuInfos[0].ModelName != "" {
		cpuModel = cpuInfos[0].ModelName
	}

	// 去除空格
	cpuModel = strings.TrimSpace(cpuModel)

	// 提取CPU核心数
	cpuCores := int32(0)
	for _, cpuInfo := range cpuInfos {
		cpuCores += cpuInfo.Cores
	}

	// 构建并返回系统信息
	return &model.SysInfo{
		Hostname:             hostInfo.Hostname,
		Uptime:               hostInfo.Uptime,
		BootTime:             hostInfo.BootTime,
		ModelName:            cpuModel,
		Cores:                cpuCores,
		Processes:            hostInfo.Procs,
		OS:                   hostInfo.OS,
		Platform:             hostInfo.Platform,
		PlatformFamily:       hostInfo.PlatformFamily,
		PlatformVersion:      hostInfo.PlatformVersion,
		KernelVersion:        hostInfo.KernelVersion,
		KernelArch:           hostInfo.KernelArch,
		VirtualizationSystem: hostInfo.VirtualizationSystem,
		VirtualizationRole:   hostInfo.VirtualizationRole,
		HostID:               hostInfo.HostID,
	}, nil
}
