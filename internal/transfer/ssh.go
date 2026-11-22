package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var shaOutputRe = regexp.MustCompile(`(?i)\b([0-9a-f]{64})\b`)

// SSHTarget streams files and executes commands on the Router host via the system ssh binary.
type SSHTarget struct {
	target    string
	sshBinary string
}

// NewSSHTarget creates an SSH-backed Target for the given user@host (or SSH config host alias).
func NewSSHTarget(target string, sshBinary string) *SSHTarget {
	if strings.TrimSpace(sshBinary) == "" {
		sshBinary = "ssh"
	}
	return &SSHTarget{
		target:    target,
		sshBinary: sshBinary,
	}
}

func (s *SSHTarget) Description() string {
	return "ssh " + s.target
}

// ShellQuote safely single-quotes a value for a POSIX shell command.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (s *SSHTarget) Stat(ctx context.Context, targetPath string) (int64, bool, error) {
	q := ShellQuote(targetPath)
	res, err := s.Run(ctx, fmt.Sprintf("if [ -f %s ]; then wc -c < %s; else echo MISSING; fi", q, q))
	if err != nil {
		return 0, false, err
	}
	if res.ExitCode != 0 {
		return 0, false, fmt.Errorf("cannot stat %s over SSH: %s", targetPath, strings.TrimSpace(res.Stderr))
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "MISSING" {
		return 0, false, nil
	}
	size, parseErr := strconv.ParseInt(out, 10, 64)
	if parseErr != nil {
		return 0, false, fmt.Errorf("unexpected stat output for %s: %q", targetPath, out)
	}
	return size, true, nil
}

func (s *SSHTarget) WriteFromReader(ctx context.Context, targetPath string, r io.Reader, appendMode bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op := ">"
	if appendMode {
		op = ">>"
	}
	remoteCmd := fmt.Sprintf("mkdir -p %s && cat %s %s", ShellQuote(path.Dir(targetPath)), op, ShellQuote(targetPath))
	cmd := exec.CommandContext(ctx, s.sshBinary, s.target, remoteCmd)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SSH write to %s failed: %s (%w)", targetPath, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func (s *SSHTarget) Rename(ctx context.Context, from, to string) error {
	remoteCmd := fmt.Sprintf("mkdir -p %s && mv -f %s %s", ShellQuote(path.Dir(to)), ShellQuote(from), ShellQuote(to))
	res, err := s.Run(ctx, remoteCmd)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cannot rename %s over SSH: %s", from, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (s *SSHTarget) Remove(ctx context.Context, targetPath string) error {
	res, err := s.Run(ctx, fmt.Sprintf("rm -f %s", ShellQuote(targetPath)))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cannot remove %s over SSH: %s", targetPath, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (s *SSHTarget) SHA256(ctx context.Context, targetPath string) (string, error) {
	q := ShellQuote(targetPath)
	remoteCmd := fmt.Sprintf("if command -v sha256sum >/dev/null 2>&1; then sha256sum %s; else shasum -a 256 %s; fi", q, q)
	res, err := s.Run(ctx, remoteCmd)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("cannot hash %s over SSH: %s", targetPath, strings.TrimSpace(res.Stderr))
	}
	m := shaOutputRe.FindStringSubmatch(res.Stdout)
	if len(m) < 2 {
		return "", fmt.Errorf("unexpected SHA-256 output for %s: %q", targetPath, strings.TrimSpace(res.Stdout))
	}
	return strings.ToLower(m[1]), nil
}

func (s *SSHTarget) ReadText(ctx context.Context, targetPath string) (string, bool, error) {
	q := ShellQuote(targetPath)
	res, err := s.Run(ctx, fmt.Sprintf("if [ -f %s ]; then cat %s; else exit 3; fi", q, q))
	if err != nil {
		return "", false, err
	}
	if res.ExitCode == 3 {
		return "", false, nil
	}
	if res.ExitCode != 0 {
		return "", false, fmt.Errorf("cannot read %s over SSH: %s", targetPath, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, true, nil
}

func (s *SSHTarget) WriteText(ctx context.Context, targetPath string, content string) error {
	return s.WriteFromReader(ctx, targetPath, strings.NewReader(content), false)
}

func (s *SSHTarget) Run(ctx context.Context, command string) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	cmd := exec.CommandContext(ctx, s.sshBinary, s.target, command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return CommandResult{}, ctx.Err()
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return CommandResult{}, err
		}
	}
	return CommandResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}
