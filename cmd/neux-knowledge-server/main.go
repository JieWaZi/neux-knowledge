// neux-knowledge-server is a thin HTTP host for reads and writes; a full business host injects model execution.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JieWaZi/neux-knowledge/core"
	"github.com/JieWaZi/neux-knowledge/httpapi"
)

// serverConfig configures managed storage and independent knowledge authentication without configuring a second CLI model host.
type serverConfig struct {
	DBPath   string                 `json:"dbPath"`   // Standalone knowledge database path.
	RawDir   string                 `json:"rawDir"`   // Managed source root directory.
	Listen   string                 `json:"listen"`   // Explicit listening address, defaulting to loopback.
	TLSCert  string                 `json:"tlsCert"`  // Optional TLS certificate file.
	TLSKey   string                 `json:"tlsKey"`   // Optional TLS private-key file.
	Security httpapi.SecurityConfig `json:"security"` // Per-library credentials and write IP policy.
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("neux-knowledge-server", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "受管服务配置 JSON 路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("必须指定 -config")
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer f.Close()
	var cfg serverConfig
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return errors.New("知识服务配置格式无效")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("知识服务配置含多余内容")
	}
	if cfg.DBPath == "" || cfg.RawDir == "" || len(cfg.Security.Tokens) == 0 {
		return errors.New("必须配置独立数据库、raw 和远端凭据")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return errors.New("TLS 证书和私钥必须同时配置")
	}
	security, err := httpapi.NewPolicyStore(cfg.Security)
	if err != nil {
		return err
	}
	engine, err := core.Open(ctx, core.Options{DBPath: cfg.DBPath, RawDir: cfg.RawDir})
	if err != nil {
		return err
	}
	defer engine.Close()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8787"
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: httpapi.New(engine, httpapi.Options{Security: security}), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	fmt.Fprintf(output, "知识服务监听 %s；整理、取消与维护未装配执行宿主\n", listener.Addr())
	if cfg.TLSCert != "" {
		err = server.ServeTLS(listener, cfg.TLSCert, cfg.TLSKey)
	} else {
		err = server.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
