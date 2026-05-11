package gitutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func sshEnv() []string {
	env := os.Environ()
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		return env
	}
	return env
}

func RunGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = sshEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("git %v: %w\n%s", args, err, buf.String())
	}
	return strings.TrimSpace(buf.String()), nil
}

func RunGitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = sshEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %v: %w\n%s", args, err, string(out))
	}
	return out, nil
}
