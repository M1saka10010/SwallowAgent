package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/M1saka10010/SwallowAgent/model"
	"github.com/M1saka10010/SwallowAgent/service"
	"github.com/M1saka10010/SwallowAgent/util"

	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("c", "", "指定 YAML 配置文件路径")
	generateKey := flag.String("generate-key", "", "独占创建 Ed25519 私钥并打印公钥")
	showKey := flag.String("show-public-key", "", "读取私钥并打印公钥")
	enrollURL := flag.String("enroll", "", "使用一次性注册令牌注册到 report URL")
	privateKey := flag.String("private-key", "", "注册使用的私钥路径，不存在则独占生成")
	tokenStdin := flag.Bool("enrollment-token-stdin", false, "从标准输入读取一次性注册令牌")
	devMode := flag.Bool("dev-mode", false, "允许 loopback 明文注册（仅开发）")
	prepare := flag.String("prepare-token-migration", "", "严格迁移旧配置（不连接服务器）")
	output := flag.String("output-config", "", "迁移配置输出路径，必须不存在")
	defaultKey := flag.String("default-private-key", "", "旧配置缺少私钥时使用的路径")
	reportURL := flag.String("report-url", "", "Token 迁移覆盖 reportUrl")
	enrollConfig := flag.String("enroll-token-migration", "", "使用已迁移配置注册或验证既有身份")
	checkToken := flag.Bool("check-enrollment-token", false, "校验 stdin 凭据并输出到 stdout（供安装器私有暂存）")
	flag.Parse()
	if *checkToken {
		if flag.NFlag() != 1 || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "凭据校验不能组合其他命令")
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		token, err := util.ReadEnrollmentTokenContext(ctx, os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(token)
		return
	}
	if *prepare != "" || *output != "" || *defaultKey != "" || *reportURL != "" || *enrollConfig != "" {
		if *configPath != "" || *generateKey != "" || *showKey != "" || *enrollURL != "" || *privateKey != "" || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "Token 迁移命令不能组合其他命令")
			os.Exit(1)
		}
		err := runTokenMigration(*prepare, *output, *defaultKey, *reportURL, *enrollConfig, *devMode, *tokenStdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *enrollURL != "" || *privateKey != "" || *tokenStdin || *devMode {
		if *enrollURL == "" || *privateKey == "" || !*tokenStdin || *configPath != "" || *generateKey != "" || *showKey != "" || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "注册需同时指定 -enroll、-private-key、-enrollment-token-stdin，不能组合其他命令")
			os.Exit(1)
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		id, err := util.Enroll(ctx, *enrollURL, *privateKey, os.Stdin, *devMode)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if id == "" {
			fmt.Println("已有私钥认证成功，设备已注册")
		} else {
			fmt.Println("注册成功，publicId:", id)
		}
		return
	}
	if *generateKey != "" || *showKey != "" {
		if (*generateKey != "" && *showKey != "") || *configPath != "" || flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "密钥命令不可与其他命令组合")
			os.Exit(1)
		}
		var publicKey string
		var err error
		if *generateKey != "" {
			publicKey, err = util.GenerateKey(*generateKey)
		} else {
			key, loadErr := util.LoadPrivateKey(*showKey)
			err = loadErr
			if err == nil {
				publicKey = util.PublicKeyString(key)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println(publicKey)
		return
	}

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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		fmt.Fprintln(os.Stderr, "配置必须只有一个 YAML 文档")
		os.Exit(1)
	}

	if err := validateConfig(&config); err != nil {
		fmt.Println("配置错误:", err)
		os.Exit(1)
	}

	key, err := util.LoadPrivateKey(config.PrivateKeyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := util.NewWSClient(config.ReportURL, key, config.DevMode)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
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
	if c.Interval == 0 || c.Interval > uint64((1<<63-1)/(20*time.Second)) {
		return fmt.Errorf("interval 必须大于 0 且不能溢出系统信息采集时长")
	}
	if strings.TrimSpace(c.ReportURL) == "" {
		return fmt.Errorf("reportUrl 不能为空")
	}
	if err := util.ValidateReportURL(c.ReportURL, c.DevMode); err != nil {
		return err
	}
	if strings.TrimSpace(c.PrivateKeyPath) == "" {
		return fmt.Errorf("privateKeyPath 不能为空")
	}
	return nil
}
