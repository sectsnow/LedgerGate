package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ledgergate/internal/app"
	"ledgergate/internal/backup"
)

func main() {
	if err := run(); err != nil {
		slog.Error("服务退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		flags := flag.NewFlagSet("restore", flag.ContinueOnError)
		file := flags.String("file", "", "加密备份文件")
		destination := flags.String("data-dir", "", "恢复到尚不存在的新目录")
		passwordFile := flags.String("password-file", "", "保存备份密码的文件")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *file == "" || *destination == "" || *passwordFile == "" {
			return errors.New("用法：ledgergate restore --file backup.lgb --data-dir new-data --password-file password.txt")
		}
		info, err := os.Stat(*file)
		if err != nil {
			return err
		}
		if info.Size() > backup.FileLimit {
			return errors.New("备份文件超过 4 GiB 大小限制")
		}
		archive, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer archive.Close()
		password, err := os.ReadFile(*passwordFile)
		if err != nil {
			return err
		}
		if err := backup.RestoreReader(archive, strings.TrimSpace(string(password)), *destination); err != nil {
			return err
		}
		fmt.Println("备份已校验并恢复到新目录：" + *destination)
		return nil
	}
	dataDir := os.Getenv("LEDGERGATE_DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	addr := os.Getenv("LEDGERGATE_ADDR")
	if addr == "" {
		addr = "0.0.0.0:8080"
	}
	application, err := app.Open(app.Config{DataDir: dataDir, AdminPassword: os.Getenv("LEDGERGATE_ADMIN_PASSWORD")})
	if err != nil {
		return err
	}
	defer application.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tasksDone := make(chan struct{})
	go func() { defer close(tasksDone); application.RunTasks(ctx) }()
	server := &http.Server{Addr: addr, Handler: application.Router(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("服务关闭失败", "error", err)
			server.Close()
		}
	}()
	slog.Info("LedgerGate 已启动", "address", addr, "password_file", dataDir+"/admin-password")
	serveError := server.ListenAndServe()
	stop()
	<-tasksDone
	<-shutdownDone
	if serveError != nil && !errors.Is(serveError, http.ErrServerClosed) {
		return serveError
	}
	return nil
}
