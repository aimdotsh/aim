package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseRequest() Request {
	return Request{
		Protocol:  ProtocolVersion,
		RequestID: "job-12345678",
		Action:    "install",
		Version:   "8.0.46",
		Port:      8046,
		Role:      "standalone",
		Secrets: Secrets{
			RootPassword: "root-secret",
		},
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		AimPath:     filepath.Join(root, "aim.sh"),
		BaseRoot:    filepath.Join(root, "opt/mysql"),
		DataRoot:    filepath.Join(root, "data/mysql"),
		LogRoot:     filepath.Join(root, "log/mysql"),
		TmpRoot:     filepath.Join(root, "tmp/mysql"),
		StagingRoot: filepath.Join(root, "staging"),
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuildCommandDoesNotExposeSecrets(t *testing.T) {
	cfg := testConfig(t)
	req := baseRequest()
	args, env, err := BuildCommand(req, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), req.Secrets.RootPassword) {
		t.Fatal("secret was placed in command-line arguments")
	}
	if !strings.Contains(strings.Join(env, "\n"), "AIM_ROOT_PASSWORD="+req.Secrets.RootPassword) {
		t.Fatal("secret environment was not generated")
	}
	if !strings.Contains(strings.Join(args, " "), "--no-print-secrets") {
		t.Fatal("secret suppression was not enabled")
	}
}

func TestDestructiveActionRequiresPreviewOrConfirmation(t *testing.T) {
	cfg := testConfig(t)
	req := baseRequest()
	req.Action = "uninstall"
	if err := ValidateRequest(req, cfg); err == nil {
		t.Fatal("unconfirmed uninstall was accepted")
	}
	req.DryRun = true
	if err := ValidateRequest(req, cfg); err != nil {
		t.Fatalf("dry-run uninstall was rejected: %v", err)
	}
}

func TestArchiveChecksumAndSymlinkBoundary(t *testing.T) {
	cfg := testConfig(t)
	req := baseRequest()
	content := append([]byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}, []byte("test archive")...)
	sum := sha256.Sum256(content)
	req.Archive = &Archive{Name: "mysql-8.0.46-linux-glibc2.17-x86_64.tar.xz", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), Version: "8.0.46", Glibc: "2.17", Architecture: "x86_64"}
	dir := filepath.Join(cfg.StagingRoot, req.RequestID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, req.Archive.Name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequest(req, cfg); err != nil {
		t.Fatalf("valid staged archive was rejected: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequest(req, cfg); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("tampered archive was accepted: %v", err)
	}

	outside := filepath.Join(t.TempDir(), req.Archive.Name)
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequest(req, cfg); err == nil || !strings.Contains(err.Error(), "escaped") {
		t.Fatalf("archive symlink escape was accepted: %v", err)
	}
}

func TestArchiveMetadataAndHostCompatibility(t *testing.T) {
	archive := Archive{Name: "mysql-8.0.46-linux-glibc2.28-aarch64-minimal.tar.xz", Size: 1024, SHA256: strings.Repeat("a", 64), Version: "8.0.46", Glibc: "2.28", Architecture: "aarch64", Minimal: true}
	if err := validateArchiveMetadata(archive, "8.0.46"); err != nil {
		t.Fatalf("valid archive metadata was rejected: %v", err)
	}
	if err := validateArchiveHostCompatibility(archive, HostFacts{Architecture: "arm64", Glibc: "2.31"}); err != nil {
		t.Fatalf("compatible host was rejected: %v", err)
	}
	if err := validateArchiveHostCompatibility(archive, HostFacts{Architecture: "amd64", Glibc: "2.31"}); err == nil {
		t.Fatal("wrong host architecture was accepted")
	}
	if err := validateArchiveHostCompatibility(archive, HostFacts{Architecture: "arm64", Glibc: "2.17"}); err == nil {
		t.Fatal("older host glibc was accepted")
	}
	archive.Version = "8.0.45"
	if err := validateArchiveMetadata(archive, "8.0.46"); err == nil {
		t.Fatal("mismatched archive metadata was accepted")
	}
}

func TestStrictRequestDecoding(t *testing.T) {
	_, err := DecodeRequest(bytes.NewBufferString(`{"protocol":1,"request_id":"job-12345678","action":"probe","unknown":true}`))
	if err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
}

func TestMGRRequiresExactlyThreeSeeds(t *testing.T) {
	cfg := testConfig(t)
	req := baseRequest()
	req.Role = "mgr"
	req.ServerID = 14690
	req.MGR = MGR{
		LocalAddress: "172.20.23.90",
		Port:         33061,
		Seeds:        []string{"172.20.23.90:33061", "172.20.23.95:33061"},
		GroupName:    "b32b3ad1-031b-4c53-bfd4-1ea75424021a",
		Allowlist:    "172.20.23.0/24",
	}
	req.Secrets.MGRRecoveryPassword = "recovery-secret"
	if err := ValidateRequest(req, cfg); err == nil {
		t.Fatal("MGR request with fewer than three seeds was accepted")
	}
}

func TestProbeDetectsPortBoundOnAnyAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	facts, err := ProbeHost([]int{port}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if facts.Ports[port] != "listening" {
		t.Fatalf("bound port was reported as %q", facts.Ports[port])
	}
}
