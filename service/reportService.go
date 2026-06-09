package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/M1saka10010/SwallowAgent/model"
	"github.com/M1saka10010/SwallowAgent/util"
)

// 出错后重试前的等待时间，避免错误时忙循环占满 CPU
const retryDelay = 5 * time.Second

// 系统信息变化较慢，按使用情况采集间隔的倍数上报
const systemInfoIntervalFactor = 20

// sleepOrDone 等待指定时长，若 ctx 被取消则提前返回 false
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ReportSystemInfo 周期性采集并上报系统信息
func ReportSystemInfo(ctx context.Context, client *util.WSClient, interval uint64, isDebug bool, wg *sync.WaitGroup) {
	defer wg.Done()

	reportInterval := time.Duration(interval) * time.Second * systemInfoIntervalFactor

	for {
		if ctx.Err() != nil {
			return
		}

		// 采集失败时退避重试，避免忙循环
		systemInfo, err := util.GetSystemInfo()
		if err != nil {
			fmt.Println(err)
			if !sleepOrDone(ctx, retryDelay) {
				return
			}
			continue
		}

		if err := client.Send(ctx, "system_info", systemInfo); err != nil {
			fmt.Println(err)
			if !sleepOrDone(ctx, retryDelay) {
				return
			}
			continue
		}

		if isDebug {
			fmt.Println("已上报系统信息")
		}

		if !sleepOrDone(ctx, reportInterval) {
			return
		}
	}
}

// ReportSystemUsage 周期性采集并上报系统使用情况
func ReportSystemUsage(ctx context.Context, client *util.WSClient, paths []string, interfaceNames []string, interval uint64, isDebug bool, wg *sync.WaitGroup) {
	defer wg.Done()

	var lastUsage *model.UsageInfo
	reportInterval := time.Duration(interval) * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		systemUsage, err := util.GetUsageInfo(lastUsage, paths, interfaceNames)
		if err != nil {
			fmt.Println(err)
			if !sleepOrDone(ctx, retryDelay) {
				return
			}
			continue
		}

		if err := client.Send(ctx, "system_usage", systemUsage); err != nil {
			fmt.Println(err)
			if !sleepOrDone(ctx, retryDelay) {
				return
			}
			continue
		}

		if isDebug {
			fmt.Println("已上报系统使用情况")
		}

		// 仅在成功上报后更新基准，保证网速计算的时间差正确
		lastUsage = systemUsage

		if !sleepOrDone(ctx, reportInterval) {
			return
		}
	}
}
