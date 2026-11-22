package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// makeFakeSSH creates a tiny POSIX sh script acting as `ssh <host> <cmd>` by running `<cmd>` locally.
func makeFakeSSH(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-ssh")
	body := "#!/bin/sh\nshift\nexec sh -c \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestDownloadQuantOverSSHStreamsVerifiesAndReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("gguf-quant-data-"), 4096) // 64 KiB
	wantSHA := sha256Hex(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Linked-Etag", `"`+wantSHA+`"`)
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ggufDir := filepath.Join(t.TempDir(), "gguf")
	target := NewSSHTarget("justin@router", makeFakeSSH(t))

	var updates []Progress
	res, err := DownloadQuant(context.Background(), DownloadOptions{
		Files: []DownloadFile{
			{
				Path: "subdir/Qwen3-Q4_K_M.gguf",
				Size: int64(len(payload)),
				URL:  srv.URL + "/Qwen3-Q4_K_M.gguf",
			},
		},
		Target:     target,
		Directory:  ggufDir,
		HTTPClient: srv.Client(),
		OnProgress: func(p Progress) { updates = append(updates, p) },
	})
	if err != nil {
		t.Fatalf("unexpected download error: %v", err)
	}

	dest := filepath.Join(ggufDir, "Qwen3-Q4_K_M.gguf")
	if len(res.Files) != 1 || res.Files[0].Path != dest {
		t.Fatalf("unexpected result files: %+v", res.Files)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("downloaded payload does not match source")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatal("expected .part file to be promoted/removed after completion")
	}
	if len(updates) == 0 || updates[len(updates)-1].Transferred != int64(len(payload)) {
		t.Fatalf("expected progress updates reaching total bytes, got %+v", updates)
	}
}

func TestDownloadQuantRequiresOverwriteConfirmation(t *testing.T) {
	ggufDir := t.TempDir()
	existing := filepath.Join(ggufDir, "model-Q4_K_M.gguf")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := NewSSHTarget("justin@router", makeFakeSSH(t))
	_, err := DownloadQuant(context.Background(), DownloadOptions{
		Files: []DownloadFile{
			{Path: "model-Q4_K_M.gguf", Size: 3, URL: "http://127.0.0.1:1/unused"},
		},
		Target:    target,
		Directory: ggufDir,
		Overwrite: false,
	})
	var owErr *OverwriteRequiredError
	if !errors.As(err, &owErr) {
		t.Fatalf("expected OverwriteRequiredError, got %v", err)
	}
	if len(owErr.Paths) != 1 || owErr.Paths[0] != existing {
		t.Fatalf("unexpected overwrite paths: %+v", owErr.Paths)
	}
}

func TestDownloadQuantResumesInterruptedTransfer(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 2048) // 32 KiB
	wantSHA := sha256Hex(payload)
	splitAt := len(payload) / 2

	var rangeHeaderSeen atomic.Bool
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("X-Linked-Etag", wantSHA)
			w.WriteHeader(http.StatusOK)
			return
		}
		n := attempts.Add(1)
		if n == 1 {
			// Simulate mid-transfer interruption after half the file.
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:splitAt])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}

		rng := r.Header.Get("Range")
		if rng == fmt.Sprintf("bytes=%d-", splitAt) {
			rangeHeaderSeen.Store(true)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", splitAt, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[splitAt:])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ggufDir := t.TempDir()
	target := NewSSHTarget("justin@router", makeFakeSSH(t))
	file := DownloadFile{
		Path:   "resume-Q4_K_M.gguf",
		Size:   int64(len(payload)),
		SHA256: wantSHA,
		URL:    srv.URL + "/resume.gguf",
	}

	// First attempt fails mid-stream, leaving .part on disk for resume.
	if _, err := DownloadQuant(context.Background(), DownloadOptions{
		Files:      []DownloadFile{file},
		Target:     target,
		Directory:  ggufDir,
		HTTPClient: srv.Client(),
	}); err == nil {
		t.Fatal("expected first interrupted download to fail")
	}

	// Second attempt resumes from splitAt using Range header.
	if _, err := DownloadQuant(context.Background(), DownloadOptions{
		Files:      []DownloadFile{file},
		Target:     target,
		Directory:  ggufDir,
		HTTPClient: srv.Client(),
	}); err != nil {
		t.Fatalf("expected resumed download to succeed, got %v", err)
	}
	if !rangeHeaderSeen.Load() {
		t.Fatal("expected HTTP Range header on resume attempt")
	}
	got, err := os.ReadFile(filepath.Join(ggufDir, "resume-Q4_K_M.gguf"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("resumed file contents do not match payload")
	}
}

func TestDownloadQuantCancelCleansUpPartialFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
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

	ggufDir := t.TempDir()
	target := NewSSHTarget("justin@router", makeFakeSSH(t))

	go func() {
		<-started
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := DownloadQuant(ctx, DownloadOptions{
		Files: []DownloadFile{
			{Path: "cancel-Q4_K_M.gguf", Size: 1048576, SHA256: strings.Repeat("a", 64), URL: srv.URL + "/cancel.gguf"},
		},
		Target:     target,
		Directory:  ggufDir,
		HTTPClient: srv.Client(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	partPath := filepath.Join(ggufDir, "cancel-Q4_K_M.gguf.part")
	if _, statErr := os.Stat(partPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected .part file to be removed on cancellation, stat err=%v", statErr)
	}
}

func TestDownloadQuantChecksumMismatchDiscardsFile(t *testing.T) {
	payload := []byte("corrupted-gguf-bytes")
	wrongSHA := strings.Repeat("f", 64)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ggufDir := t.TempDir()
	target := NewSSHTarget("justin@router", makeFakeSSH(t))

	_, err := DownloadQuant(context.Background(), DownloadOptions{
		Files: []DownloadFile{
			{Path: "bad-Q4_K_M.gguf", Size: int64(len(payload)), SHA256: wrongSHA, URL: srv.URL + "/bad.gguf"},
		},
		Target:     target,
		Directory:  ggufDir,
		HTTPClient: srv.Client(),
	})
	var csErr *ChecksumError
	if !errors.As(err, &csErr) {
		t.Fatalf("expected ChecksumError, got %v", err)
	}

	for _, name := range []string{"bad-Q4_K_M.gguf", "bad-Q4_K_M.gguf.part"} {
		if _, statErr := os.Stat(filepath.Join(ggufDir, name)); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be discarded after checksum mismatch", name)
		}
	}
}
