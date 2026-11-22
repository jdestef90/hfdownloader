package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// LocalTarget writes and verifies files directly on the Router host filesystem without SSH.
type LocalTarget struct{}

// NewLocalTarget creates a direct local-filesystem Target.
func NewLocalTarget() *LocalTarget {
	return &LocalTarget{}
}

func (l *LocalTarget) Description() string {
	return "local"
}

func (l *LocalTarget) Stat(ctx context.Context, targetPath string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if info.IsDir() {
		return 0, false, fmt.Errorf("%s is a directory, expected a file", targetPath)
	}
	return info.Size(), true, nil
}

func (l *LocalTarget) WriteFromReader(ctx context.Context, targetPath string, r io.Reader, appendMode bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(targetPath, flags, 0o644)
	if err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				_ = f.Close()
				return writeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = f.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
	}
	return f.Close()
}

func (l *LocalTarget) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}

func (l *LocalTarget) Remove(ctx context.Context, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(targetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *LocalTarget) SHA256(ctx context.Context, targetPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(targetPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (l *LocalTarget) ReadText(ctx context.Context, targetPath string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func (l *LocalTarget) WriteText(ctx context.Context, targetPath string, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetPath, []byte(content), 0o644)
}

func (l *LocalTarget) Run(ctx context.Context, command string) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
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
