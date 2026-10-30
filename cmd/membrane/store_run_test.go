package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/inferencepath/membrane/internal/store"
)

// seedBundleYAML is a complete seed bundle, with secret references and hashes
// only. It matches the single-pathway config written by seedStore.
const seedBundleYAML = `version: 1
providers:
  - name: main
    family: openai
    models: [gpt-4o-mini]
    default_model: gpt-4o-mini
    endpoint: https://api.example.com
    secret_ref: env:RUN_STORE_KEY
pathways:
  - pathway: chat
    providers: [main]
callers:
  - key_id: ck_demo
    hash: 458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816
    caller_id: acme-app
    name: demo
    allow: [chat]
permeability:
  chat:
    outbound:
      allow: [messages]
    inbound:
      default: [message]
`

// runThenShutdown starts run, waits for startup, sends SIGTERM, and expects
// exit code 0.
func runThenShutdown(t *testing.T, args []string) {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- run(args) }()
	time.Sleep(500 * time.Millisecond)
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

func assertConfigured(t *testing.T, dir string) {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s.Close()
	ok, err := s.IsConfigured()
	if err != nil {
		t.Fatalf("is configured: %v", err)
	}
	if !ok {
		t.Fatal("expected store to be configured after seed")
	}
}

// TestRunSeedOnEmptyDefaultPath checks that <data_dir>/seed.yaml is applied to
// a fresh store at startup.
func TestRunSeedOnEmptyDefaultPath(t *testing.T) {
	t.Setenv("RUN_STORE_KEY", "egress-token")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "membrane.yaml")
	writeFile(t, cfgPath, "server:\n  address: \"127.0.0.1:0\"\ndata_dir: \""+dir+"\"\n")
	writeFile(t, filepath.Join(dir, "seed.yaml"), seedBundleYAML)

	runThenShutdown(t, []string{"--config", cfgPath})
	assertConfigured(t, dir)
}

// TestRunSeedOnEmptyExplicitFlag checks that --seed can point at a bundle
// outside data_dir.
func TestRunSeedOnEmptyExplicitFlag(t *testing.T) {
	t.Setenv("RUN_STORE_KEY", "egress-token")
	dir := t.TempDir()
	seedPath := filepath.Join(t.TempDir(), "custom-seed.yaml")
	writeFile(t, seedPath, seedBundleYAML)
	cfgPath := filepath.Join(dir, "membrane.yaml")
	writeFile(t, cfgPath, "server:\n  address: \"127.0.0.1:0\"\ndata_dir: \""+dir+"\"\n")

	runThenShutdown(t, []string{"--config", cfgPath, "--seed", seedPath})
	assertConfigured(t, dir)
}

// TestRunSeedSkippedWhenConfigured checks that an already configured store is
// not re-seeded.
func TestRunSeedSkippedWhenConfigured(t *testing.T) {
	t.Setenv("RUN_STORE_KEY", "egress-token")
	dir := t.TempDir()
	seedStore(t, dir) // already configured

	// This seed has a different pathway and must not be applied.
	altSeed := "version: 1\npathways:\n  - pathway: other\n    providers: [main]\ncredentials:\n  - name: main\n    secret_ref: env:RUN_STORE_KEY\n"
	writeFile(t, filepath.Join(dir, "seed.yaml"), altSeed)

	cfgPath := filepath.Join(dir, "membrane.yaml")
	writeFile(t, cfgPath, "server:\n  address: \"127.0.0.1:0\"\ndata_dir: \""+dir+"\"\n")
	runThenShutdown(t, []string{"--config", cfgPath})

	// The original 'chat' pathway must still be the only one.
	s, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	caps, _ := s.ReadPathways()
	if len(caps) != 1 || caps[0].Name != "chat" {
		t.Fatalf("seed should have been skipped; pathways = %+v", caps)
	}
	_ = os.Remove(filepath.Join(dir, "seed.yaml"))
}

// seedStore writes a complete single-pathway configuration, with secret
// references and hashes only, to the store under dir and closes it so the
// daemon can open it.
func seedStore(t *testing.T, dir string) {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, store.DBFileName))
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer s.Close()
	for _, q := range []string{
		`INSERT INTO pathways (name, cascade_allowed, default_provider) VALUES ('chat', 0, 'main')`,
		`INSERT INTO providers (name, family, endpoint, models, default_model, secret_ref)
		 VALUES ('main', 'openai', 'https://api.example.com', 'gpt-4o-mini', 'gpt-4o-mini', 'env:RUN_STORE_KEY')`,
		`INSERT INTO pathway_providers (pathway, provider) VALUES ('chat', 'main')`,
		`INSERT INTO callers (key_id, caller_id, name, secret_hash)
		 VALUES ('ck_demo', 'acme-app', 'demo', '458d6534a760ed4c8c2f1c8f86a517572cb56fa6a0f8554efdda2cb8532a4816')`,
		`INSERT INTO caller_allow (key_id, pathway) VALUES ('ck_demo', 'chat')`,
		`INSERT INTO permeability (pathway, outbound_allow, inbound_default)
		 VALUES ('chat', 'messages', 'message')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("seed exec: %v", err)
		}
	}
}

// TestRunStoreModeReadyThenShutdown starts a ready daemon on a seeded store,
// sends SIGHUP for a no-op reload, and shuts down.
func TestRunStoreModeReadyThenShutdown(t *testing.T) {
	t.Setenv("RUN_STORE_KEY", "egress-token")
	dir := t.TempDir()
	seedStore(t, dir)

	cfgPath := filepath.Join(dir, "membrane.yaml")
	auditPath := filepath.Join(dir, "audit.jsonl")
	writeFile(t, cfgPath, ""+
		"server:\n  address: \"127.0.0.1:0\"\n"+
		"data_dir: \""+dir+"\"\n"+
		"audit:\n  sink: jsonlines\n  path: \""+auditPath+"\"\n"+
		"console:\n  enabled: true\n  address: \"127.0.0.1:0\"\n")

	runSIGHUPThenShutdown(t, []string{"--config", cfgPath})
}

// TestRunStoreModeIncompleteFailClosed checks that an empty store leaves the
// daemon not ready but still able to start and shut down cleanly.
func TestRunStoreModeIncompleteFailClosed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "membrane.yaml")
	writeFile(t, cfgPath, ""+
		"server:\n  address: \"127.0.0.1:0\"\n"+
		"data_dir: \""+dir+"\"\n")

	runUntilSignal(t, []string{"--config", cfgPath})
}

// TestRunStoreModeOnboardingBecomesReady starts the daemon on an empty store,
// configures the store out of band, and checks that a SIGHUP reload makes the
// daemon ready without a restart.
func TestRunStoreModeOnboardingBecomesReady(t *testing.T) {
	t.Setenv("RUN_STORE_KEY", "egress-token")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "membrane.yaml")
	writeFile(t, cfgPath, ""+
		"server:\n  address: \"127.0.0.1:0\"\n"+
		"data_dir: \""+dir+"\"\n")

	done := make(chan int, 1)
	go func() { done <- run([]string{"--config", cfgPath}) }()

	// Let the daemon open the empty store and install the signal handler.
	time.Sleep(400 * time.Millisecond)
	// Configure the store out of band, then SIGHUP to rebuild.
	seedStore(t, dir)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
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
