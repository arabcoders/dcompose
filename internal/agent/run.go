package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var version = "dev"

// GenerateCert writes a new TLS key/certificate pair without overwriting files.
func GenerateCert(dir string) (string, error) { return generateCert(dir) }

// GenerateToken writes a new private bearer token without overwriting files.
func GenerateToken(path string) error { return generateToken(path) }

// Run serves an agent until cancellation or a server error.
func Run(parent context.Context, configPath, buildVersion string) error {
	version = buildVersion
	c, err := loadConfig(configPath)
	if nil != err {
		return err
	}
	_, fingerprint, err := connectionInfo(c, configPath)
	if nil != err {
		return fmt.Errorf("read node connection information: %w", err)
	}
	ctx, stop := context.WithCancel(parent)
	defer stop()
	a := newAgent(ctx, c)
	restoreLogs := a.forwardLogs()
	defer restoreLogs()
	if err := a.initRegistries(filepath.Dir(configPath)); nil != err {
		return err
	}
	if err := a.engine.Check(ctx); nil != err {
		return err
	}
	if cacheDir, err := os.UserCacheDir(); nil != err {
		slog.Warn(fmt.Sprintf("Could not locate the node operation storage directory: %v. Detached stack operations are unavailable.", err), "error", err)
		a.initDetached("")
	} else {
		a.initDetached(cacheDir)
	}
	a.startBackground()
	server := &http.Server{
		Addr: c.Global.Listen, Handler: a, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		TLSConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		BaseContext: func(net.Listener) context.Context { return ctx },
		ErrorLog:    slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); nil != err {
			slog.Warn(fmt.Sprintf("Could not finish node shutdown at %s: %v. Closing active connections.", c.Global.Listen, err), "address", c.Global.Listen, "error", err)
			_ = server.Close()
		}
	}()
	fmt.Fprintf(os.Stderr, "TLS fingerprint: %s\n", fingerprint)
	if "" != a.pairingToken {
		fmt.Fprintf(os.Stderr, "Use this single-use pairing token in Nodes > Add node: %s\n", a.pairingToken)
	}
	slog.Info(fmt.Sprintf("Starting DCompose node %s at https://%s with stacks in %q and configuration %q.", version, c.Global.Listen, c.Global.StacksDir, configPath), "version", version, "address", "https://"+c.Global.Listen, "stacks_dir", c.Global.StacksDir, "config", configPath)
	err = server.ListenAndServeTLS(c.TLS.CertFile, c.TLS.KeyFile)
	// Also stop background work when bind/TLS startup fails.
	stop()
	<-shutdownDone
	a.work.Wait()
	if nil != err && http.ErrServerClosed != err {
		return fmt.Errorf("serve node: %w", err)
	}
	return nil
}
