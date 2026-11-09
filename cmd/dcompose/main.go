package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arabcoders/dcompose/frontend"
	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/logging"
	"github.com/arabcoders/dcompose/internal/server"
	"golang.org/x/crypto/bcrypt"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "TOML configuration file")
	showVersion := flag.Bool("version", false, "Print version, then exit")
	hashPassword := flag.Bool("hash-password", false, "Print a password's bcrypt hash")
	setPassword := flag.Bool("set-password", false, "Set the account password")
	username := flag.String("username", "", "Username for --set-password")
	logLevel := flag.String("log-level", "", "Override log level: debug, info, warn, error")
	flag.Parse()
	var activeLevel slog.LevelVar
	slog.SetDefault(slog.New(logging.NewConsoleHandler(os.Stderr, &activeLevel)))
	level, err := configutil.LogLevel("", *logLevel)
	if nil != err {
		slog.Error(fmt.Sprintf("Could not configure logging: %v", err), "error", err)
		os.Exit(1)
	}
	activeLevel.Set(level)
	if *showVersion {
		fmt.Printf("dcompose %s\n", version)
		return
	}
	if *hashPassword || *setPassword {
		password, err := readPassword(os.Stdin, os.Stderr, *setPassword)
		if nil != err {
			slog.Error(fmt.Sprintf("Could not read the account password: %v", err), "error", err)
			os.Exit(1)
		}
		if *setPassword {
			path, err := configutil.Find(*configPath, "dcompose.toml")
			if nil != err {
				slog.Error(fmt.Sprintf("Could not determine the DCompose configuration path: %v", err), "error", err)
				os.Exit(1)
			}
			level, err = configutil.LogLevel(path, *logLevel)
			if nil != err {
				slog.Error(fmt.Sprintf("Could not configure logging from %q: %v", path, err), "config", path, "error", err)
				os.Exit(1)
			}
			activeLevel.Set(level)
			if err := server.SetPassword(path, *username, password); nil != err {
				slog.Error(fmt.Sprintf("Could not save account credentials in %q: %v", path, err), "config", path, "error", err)
				os.Exit(1)
			}
			fmt.Println("Account credentials saved.")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if nil != err {
			slog.Error(fmt.Sprintf("Could not hash the account password: %v", err), "error", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}
	path, err := configutil.Find(*configPath, "dcompose.toml")
	if nil != err {
		slog.Error(fmt.Sprintf("Could not determine the DCompose configuration path: %v", err), "error", err)
		os.Exit(1)
	}
	level, err = configutil.LogLevel(path, *logLevel)
	if nil != err {
		slog.Error(fmt.Sprintf("Could not configure logging from %q: %v", path, err), "config", path, "error", err)
		os.Exit(1)
	}
	activeLevel.Set(level)
	config, err := server.LoadConfig(path)
	if nil != err {
		slog.Error(fmt.Sprintf("Could not load DCompose configuration %q: %v", path, err), "config", path, "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app := server.New(config, frontend.Assets(), version)
	if err := app.StartLogHistory(); nil != err {
		slog.Error(
			fmt.Sprintf(
				"Could not open operational log history for configuration %q: %v. Make the configuration logs directory writable by the runtime user.",
				path,
				err,
			),
			"config",
			path,
			"error",
			err,
		)
		os.Exit(1)
	}
	app.Start(ctx)
	defer app.Close()
	httpServer := &http.Server{
		Addr: config.Global.Listen, Handler: app, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); nil != err {
			slog.Warn(
				fmt.Sprintf("Could not finish shutting down DCompose at %s: %v. Closing active connections.", config.Global.Listen, err),
				"address",
				config.Global.Listen,
				"error",
				err,
			)
			_ = httpServer.Close()
		}
	}()
	slog.Info(
		fmt.Sprintf("Starting DCompose %s at http://%s with configuration %q.", version, config.Global.Listen, path),
		"version",
		version,
		"address",
		"http://"+config.Global.Listen,
		"config",
		path,
	)
	if err := httpServer.ListenAndServe(); nil != err && http.ErrServerClosed != err {
		slog.Error(
			fmt.Sprintf("DCompose could not serve HTTP requests at %s: %v", config.Global.Listen, err),
			"address",
			config.Global.Listen,
			"error",
			err,
		)
		app.Close()
		os.Exit(1)
	}
}
