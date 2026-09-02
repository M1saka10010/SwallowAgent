package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/M1saka10010/SwallowAgent/model"
	"github.com/M1saka10010/SwallowAgent/service"
	"github.com/M1saka10010/SwallowAgent/util"

	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("c", "", "指定 YAML 配置文件路径")
	flag.Parse()

	if *configPath == "" {
		fmt.Println("请使用 -c 指定 YAML 配置文件路径")
		os.Exit(1)
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	var config model.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if err := validateConfig(&config); err != nil {
		fmt.Println("配置错误:", err)
		os.Exit(1)
	}

	// 请求头，连接时携带鉴权 Token
	headers := map[string]string{
		"Token": config.Token,
	}

	client := util.NewWSClient(config.ReportURL, headers)
	defer client.Close()

	// 监听中断信号，触发优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)

	go service.ReportSystemInfo(ctx, client, config.Interval, config.IsDebug, &wg)
	go service.ReportSystemUsage(ctx, client, config.Paths, config.InterfaceNames, config.Interval, config.IsDebug, &wg)

	wg.Wait()
}

// validateConfig 校验配置有效性
func validateConfig(c *model.Config) error {
	if c.Interval == 0 {
		return fmt.Errorf("interval 必须大于 0")
	}
	if strings.TrimSpace(c.ReportURL) == "" {
		return fmt.Errorf("reportUrl 不能为空")
	}
	u, err := url.Parse(c.ReportURL)
	if err != nil {
		return fmt.Errorf("reportUrl 无效: %w", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("reportUrl 必须使用 ws:// 或 wss:// 协议")
	}
	if u.Scheme == "ws" {
		fmt.Println("[安全警告] 正在使用明文 ws:// 协议，Token 将以明文传输，建议在生产环境使用 wss://")
	}
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("token 不能为空")
	}
	return nil
}
