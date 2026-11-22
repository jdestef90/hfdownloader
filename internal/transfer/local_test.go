package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalTargetDownloadParityWithSSH(t *testing.T) {
	payload := bytes.Repeat([]byte("router-local-quant-"), 2048)
	wantSHA := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	localDir := filepath.Join(t.TempDir(), "gguf")
	sshDir := filepath.Join(t.TempDir(), "gguf")

	localRes, err := DownloadQuant(context.Background(), DownloadOptions{
		Files: []DownloadFile{
			{Path: "Q4_K_M/Qwen3-Q4_K_M.gguf", Size: int64(len(payload)), SHA256: wantSHA, URL: srv.URL + "/m.gguf"},
		},
		Target:     NewLocalTarget(),
		Directory:  localDir,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("local download failed: %v", err)
	}

	sshRes, err := DownloadQuant(context.Background(), DownloadOptions{
		Files: []DownloadFile{
			{Path: "Q4_K_M/Qwen3-Q4_K_M.gguf", Size: int64(len(payload)), SHA256: wantSHA, URL: srv.URL + "/m.gguf"},
		},
		Target:     NewSSHTarget("justin@router", makeFakeSSH(t)),
		Directory:  sshDir,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("ssh download failed: %v", err)
	}

	if filepath.Base(localRes.Files[0].Path) != filepath.Base(sshRes.Files[0].Path) {
		t.Fatalf("filename mismatch between local (%s) and ssh (%s)", localRes.Files[0].Path, sshRes.Files[0].Path)
	}
	if localRes.Files[0].Path != filepath.Join(localDir, "Qwen3-Q4_K_M.gguf") {
		t.Fatalf("unexpected local path: %s", localRes.Files[0].Path)
	}

	localBytes, err := os.ReadFile(localRes.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(localBytes, payload) {
		t.Fatal("local file content mismatch")
	}
}

func TestLocalTargetResumeCancelAndChecksumMismatch(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		payload := bytes.Repeat([]byte("local-resume-data"), 1024)
		wantSHA := sha256Hex(payload)
		splitAt := len(payload) / 2
		var attempts atomic.Int32
		var resumed atomic.Bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(payload[:splitAt])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
					}
				}
				return
			}
			if r.Header.Get("Range") == fmt.Sprintf("bytes=%d-", splitAt) {
				resumed.Store(true)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", splitAt, len(payload)-1, len(payload)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[splitAt:])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dir := t.TempDir()
		f := DownloadFile{Path: "r.gguf", Size: int64(len(payload)), SHA256: wantSHA, URL: srv.URL + "/r.gguf"}
		if _, err := DownloadQuant(context.Background(), DownloadOptions{
			Files: []DownloadFile{f}, Target: NewLocalTarget(), Directory: dir, HTTPClient: srv.Client(),
		}); err == nil {
			t.Fatal("expected initial interrupted download to fail")
		}
		if _, err := DownloadQuant(context.Background(), DownloadOptions{
			Files: []DownloadFile{f}, Target: NewLocalTarget(), Directory: dir, HTTPClient: srv.Client(),
		}); err != nil {
			t.Fatalf("expected resumed local download to succeed, got %v", err)
		}
		if !resumed.Load() {
			t.Fatal("expected HTTP Range resume on local target")
		}
	})

	t.Run("cancel removes part file", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1048576")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("z"), 4096))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-started:
			default:
				close(started)
			}
			<-r.Context().Done()
		}))
		defer srv.Close()

		dir := t.TempDir()
		go func() {
			<-started
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		_, err := DownloadQuant(ctx, DownloadOptions{
			Files: []DownloadFile{
				{Path: "c.gguf", Size: 1048576, SHA256: strings.Repeat("a", 64), URL: srv.URL + "/c.gguf"},
			},
			Target:     NewLocalTarget(),
			Directory:  dir,
			HTTPClient: srv.Client(),
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "c.gguf.part")); !os.IsNotExist(statErr) {
			t.Fatalf("expected .part file removed on local cancel, stat err=%v", statErr)
		}
	})

	t.Run("checksum mismatch discards file", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("bad-local"))
		}))
		defer srv.Close()

		dir := t.TempDir()
		_, err := DownloadQuant(context.Background(), DownloadOptions{
			Files: []DownloadFile{
				{Path: "bad.gguf", Size: 9, SHA256: strings.Repeat("0", 64), URL: srv.URL + "/bad.gguf"},
			},
			Target:     NewLocalTarget(),
			Directory:  dir,
			HTTPClient: srv.Client(),
		})
		var csErr *ChecksumError
		if !errors.As(err, &csErr) {
			t.Fatalf("expected ChecksumError, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "bad.gguf")); !os.IsNotExist(statErr) {
			t.Fatal("expected mismatched local file to be discarded")
		}
	})
}
