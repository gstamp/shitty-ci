package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"shitty-ci/internal/config"
	"shitty-ci/internal/proto"
	"shitty-ci/internal/xdg"
)

var (
	ServerAddr string
	AuthToken  string
)

func dial() (*net.UnixConn, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}
	dataDir := xdg.DataDir(cfg.DataDir)
	sock := xdg.SocketPath(dataDir)
	addr, err := net.ResolveUnixAddr("unix", sock)
	if err != nil {
		return nil, err
	}
	c, err := net.DialUnix("unix", nil, addr)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func friendlyDialErr(err error) error {
	if err == nil {
		return nil
	}
	var op *net.OpError
	if errors.As(err, &op) {
		if errors.Is(op.Err, syscall.ECONNREFUSED) || errors.Is(op.Err, syscall.ENOENT) {
			return fmt.Errorf("can't connect to shitty-ci daemon — run `shitty-ci server` first")
		}
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("can't connect to shitty-ci daemon — run `shitty-ci server` first")
	}
	return err
}

func RPC(req proto.Request) (proto.Response, error) {
	if ServerAddr != "" {
		return rpcRemote(req)
	}
	return rpcLocal(req)
}

func rpcLocal(req proto.Request) (proto.Response, error) {
	c, err := dial()
	if err != nil {
		return proto.Response{}, friendlyDialErr(err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return proto.Response{}, err
	}
	if err := c.CloseWrite(); err != nil {
		return proto.Response{}, err
	}
	var resp proto.Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return proto.Response{}, err
	}
	return resp, nil
}

func rpcRemote(req proto.Request) (proto.Response, error) {
	req.Token = AuthToken
	c, err := net.Dial("tcp", ServerAddr)
	if err != nil {
		return proto.Response{}, friendlyDialErr(err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return proto.Response{}, err
	}
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
	var resp proto.Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return proto.Response{}, err
	}
	return resp, nil
}

func InstallSystemd() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	unitDir := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unitPath := filepath.Join(unitDir, "shitty-ci.service")
	content := fmt.Sprintf(`[Unit]
Description=shitty-ci daemon
After=network-online.target

[Service]
ExecStart=%s server
Restart=on-failure

[Install]
WantedBy=default.target
`, exe)
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", unitPath)
	fmt.Println("run: systemctl --user daemon-reload && systemctl --user enable --now shitty-ci.service")
	return nil
}
