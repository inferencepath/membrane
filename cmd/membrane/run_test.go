package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunBadFlag(t *testing.T) {
	if code := run([]string{"--nope"}); code != 2 {
		t.Fatalf("run bad flag = %d, want 2", code)
	}
}

func TestRunMissingConfig(t *testing.T) {
	if code := run([]string{"--config", "/does/not/exist.yaml"}); code != 1 {
		t.Fatalf("run missing config = %d, want 1", code)
	}
}

// runUntilSignal starts run in a goroutine, sends SIGTERM once the signal
// handler should be installed, and asserts a graceful exit (code 0).
func runUntilSignal(t *testing.T, args []string) {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- run(args) }()

	// Give run time to load config and install the signal handler.
	time.Sleep(400 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("run = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("run did not exit after SIGTERM")
	}
}

// runSIGHUPThenShutdown starts run, sends SIGHUP (to trigger a Keyring
// rotation), then SIGTERM for a graceful exit (code 0).
func runSIGHUPThenShutdown(t *testing.T, args []string) {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- run(args) }()

	time.Sleep(400 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	// Let the rotation goroutine process the signal before shutting down.
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("run = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("run did not exit after SIGTERM")
	}
}

func TestRunInvalidConfigNoDataDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "membrane.yaml")
	// No data_dir → config invalid → run returns 1 (managed mode is required).
	writeFile(t, cfgPath, "server:\n  address: \"127.0.0.1:0\"\n")
	if code := run([]string{"--config", cfgPath}); code != 1 {
		t.Fatalf("run without data_dir = %d, want 1", code)
	}
}
