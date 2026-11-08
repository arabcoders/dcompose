package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/arabcoders/dcompose/internal/agent"
	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/logging"
)

var version = "dev"

func main() {
	path := flag.String("config", "", "TOML configuration file")
	certDir := flag.String("generate-cert", "", "Generate node.crt and node.key in this directory, then exit")
	tokenPath := flag.String("generate-token", "", "Write a node API key to this file, then exit")
	showVersion := flag.Bool("version", false, "Print version, then exit")
	flag.BoolVar(showVersion, "v", false, "Print version, then exit")
	logLevel := flag.String("log-level", "", "Override log level: debug, info, warn, error")
	operationFile := flag.String("run-operation", "", "Run the stack operation described in this file, then exit")
	flag.Parse()
	var activeLevel slog.LevelVar
	slog.SetDefault(slog.New(logging.NewConsoleHandler(os.Stderr, &activeLevel)))
	level, err := configutil.LogLevel("", *logLevel)
	if nil != err {
		slog.Error(fmt.Sprintf("Could not configure node command logging: %v", err), "error", err)
		os.Exit(1)
	}
	activeLevel.Set(level)
	if "" != *operationFile {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := agent.RunOperation(ctx, *operationFile); nil != err {
			slog.Error(fmt.Sprintf("Detached stack operation from request %q failed: %v", *operationFile, err), "request", *operationFile, "error", err)
			os.Exit(1)
		}
		return
	}
	if *showVersion {
		fmt.Printf("dagent %s\n", version)
		return
	}
	if "" != *certDir || "" != *tokenPath {
		if "" != *certDir {
			fingerprint, err := agent.GenerateCert(*certDir)
			if nil != err {
				slog.Error(fmt.Sprintf("Could not create the node TLS certificate and private key in %q: %v", *certDir, err), "directory", *certDir, "error", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Created the node TLS certificate at %s and private key at %s.\n", filepath.Join(*certDir, "node.crt"), filepath.Join(*certDir, "node.key"))
			fmt.Printf("TLS fingerprint: %s\n", fingerprint)
		}
		if "" != *tokenPath {
			if err := agent.GenerateToken(*tokenPath); nil != err {
				slog.Error(fmt.Sprintf("Could not create the node API key file %q: %v", *tokenPath, err), "path", *tokenPath, "error", err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "Created the node API key file at %s.\n", *tokenPath)
		}
		return
	}
	configPath, err := configutil.Find(*path, "agent.toml")
	if nil != err {
		slog.Error(fmt.Sprintf("Could not determine the node configuration path: %v", err), "error", err)
		os.Exit(1)
	}
	level, err = configutil.LogLevel(configPath, *logLevel)
	if nil != err {
		slog.Error(fmt.Sprintf("Could not configure node logging from %q: %v", configPath, err), "config", configPath, "error", err)
		os.Exit(1)
	}
	activeLevel.Set(level)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := agent.Run(ctx, configPath, version); nil != err {
		slog.Error(fmt.Sprintf("DCompose node using configuration %q failed: %v", configPath, err), "config", configPath, "error", err)
		os.Exit(1)
	}
}
