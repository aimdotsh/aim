package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aimdotsh/aim/internal/executor"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "aim.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSecretBox(t *testing.T) *SecretBox {
	t.Helper()
	box, err := NewSecretBox(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSecretEncryptionAndPasswordHash(t *testing.T) {
	box := testSecretBox(t)
	cipherText, err := box.Encrypt([]byte("database-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cipherText, "database-secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	plain, err := box.Decrypt(cipherText)
	if err != nil || string(plain) != "database-secret" {
		t.Fatal("secret round trip failed")
	}
	hash, err := HashPassword("very-strong-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "very-strong-admin-password") || VerifyPassword(hash, "wrong-password") {
		t.Fatal("Argon2id verification failed")
	}
}

func TestMediaFilenameParsing(t *testing.T) {
	tests := []struct {
		name    string
		version string
		arch    string
		format  string
	}{
		{"mysql-8.0.46-linux-glibc2.28-x86_64.tar.xz", "8.0.46", "x86_64", "tar.xz"},
		{"mysql-8.0.46-linux-glibc2.17-x86_64-minimal.tar", "8.0.46", "x86_64", "tar"},
		{"mysql-8.0.46-linux-glibc2.28-aarch64.tar.xz", "8.0.46", "aarch64", "tar.xz"},
		{"mysql-5.7.44-linux-glibc2.12-x86_64.tar.gz", "5.7.44", "x86_64", "tar.gz"},
	}
	for _, test := range tests {
		metadata, err := ParseMediaFilename(test.name)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if metadata.Version != test.version || metadata.Architecture != test.arch || metadata.Format != test.format {
			t.Fatalf("unexpected metadata for %s: %+v", test.name, metadata)
		}
	}
	for _, invalid := range []string{"mysql-test-8.0.46-linux-glibc2.28-x86_64.tar.xz", "../mysql-8.0.46-linux-glibc2.28-x86_64.tar.xz", "mysql-9.0.1-linux-glibc2.28-x86_64.zip"} {
		if _, err := ParseMediaFilename(invalid); err == nil {
			t.Fatalf("invalid package was accepted: %s", invalid)
		}
	}
}

func TestMediaCompatibility(t *testing.T) {
	media := Media{Architecture: "x86_64", Glibc: "2.17"}
	if err := mediaCompatible(media, executor.HostFacts{Architecture: "amd64", Glibc: "2.28"}); err != nil {
		t.Fatalf("compatible glibc package was rejected: %v", err)
	}
	if err := mediaCompatible(Media{Architecture: "x86_64", Glibc: "2.28"}, executor.HostFacts{Architecture: "amd64", Glibc: "2.17"}); err == nil {
		t.Fatal("package requiring newer glibc was accepted")
	}
	if err := mediaCompatible(media, executor.HostFacts{Architecture: "arm64", Glibc: "2.28"}); err == nil {
		t.Fatal("package for wrong architecture was accepted")
	}
}

func TestChunkUploadAndChecksum(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manager := UploadManager{Store: store, Root: filepath.Join(root, "uploads"), MediaRoot: filepath.Join(root, "media"), MaxSize: 2 << 30}
	content := []byte("small test package")
	id, err := manager.Create(1, "mysql-8.0.46-linux-glibc2.28-x86_64.tar.xz", int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.WriteChunk(id, 0, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	media, err := manager.Complete(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if media.Size != int64(len(content)) || len(media.SHA256) != 64 {
		t.Fatalf("unexpected completed media: %+v", media)
	}
	if _, err := os.Stat(media.Path); err != nil {
		t.Fatal(err)
	}
}

func TestMGRDeploymentValidation(t *testing.T) {
	input := DeploymentRequest{
		Name: "test-mgr", Mode: "mgr", Version: "8.0.46", Port: 8023, MGRPort: 18023, MGRAllowlist: "192.168.31.0/24",
		Nodes: []DeploymentNode{{HostID: 1, LocalIP: "192.168.31.101", ServerID: 14690}, {HostID: 2, LocalIP: "192.168.31.102", ServerID: 14695}, {HostID: 3, LocalIP: "192.168.31.103", ServerID: 14696}},
	}
	if err := validateDeployment(&input); err != nil {
		t.Fatal(err)
	}
	autoAllowlist := input
	autoAllowlist.MGRAllowlist = ""
	if err := validateDeployment(&autoAllowlist); err != nil || autoAllowlist.MGRAllowlist != "192.168.31.101,192.168.31.102,192.168.31.103" {
		t.Fatalf("MGR allowlist was not generated from exact member IPs: %q (%v)", autoAllowlist.MGRAllowlist, err)
	}
	input.Nodes[2].ServerID = input.Nodes[1].ServerID
	if err := validateDeployment(&input); err == nil {
		t.Fatal("duplicate MGR server_id was accepted")
	}
	input.Nodes[2].ServerID = 14696
	input.Version = "8.0.22"
	if err := validateDeployment(&input); err == nil {
		t.Fatal("unsupported pre-8.0.23 MGR version was accepted")
	}
}

func TestJobStateMachine(t *testing.T) {
	valid := [][2]string{{"queued", "preflight"}, {"preflight", "transferring"}, {"transferring", "running"}, {"running", "complete"}, {"running", "failed"}, {"needs_verification", "failed"}}
	for _, transition := range valid {
		if !canTransitionJobState(transition[0], transition[1]) {
			t.Fatalf("valid transition was rejected: %s -> %s", transition[0], transition[1])
		}
	}
	invalid := [][2]string{{"queued", "complete"}, {"complete", "running"}, {"failed", "running"}, {"transferring", "complete"}}
	for _, transition := range invalid {
		if canTransitionJobState(transition[0], transition[1]) {
			t.Fatalf("invalid transition was accepted: %s -> %s", transition[0], transition[1])
		}
	}
}

func TestReplicationRequiresUniqueServerIDs(t *testing.T) {
	input := DeploymentRequest{
		Name: "primary-replica", Mode: "replication", Version: "8.0.46", Port: 8023,
		Nodes: []DeploymentNode{{HostID: 1, LocalIP: "192.168.31.101", ServerID: 101}, {HostID: 2, LocalIP: "192.168.31.102", ServerID: 101}},
	}
	if err := validateDeployment(&input); err == nil {
		t.Fatal("replication with duplicate server_id was accepted")
	}
	input.Nodes[1].ServerID = 102
	if err := validateDeployment(&input); err != nil {
		t.Fatalf("replication with unique server_id was rejected: %v", err)
	}
}

func TestSessionRBACAndCSRF(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	server, err := NewServer(store, testSecretBox(t), ServerConfig{UploadRoot: filepath.Join(root, "uploads"), MediaRoot: filepath.Join(root, "media"), MaxUpload: 2 << 30})
	if err != nil {
		t.Fatal(err)
	}
	loginBody := bytes.NewBufferString(`{"username":"admin","password":"very-strong-admin-password"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session", loginBody)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", response.Code, response.Body.String())
	}
	var login map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &login)
	csrf, _ := login["csrf_token"].(string)
	cookie := response.Result().Cookies()[0]

	request = httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(`{"username":"viewer1","password":"viewer-password-123","role":"viewer"}`))
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("write without CSRF was not rejected: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(`{"username":"viewer1","password":"viewer-password-123","role":"viewer"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	response = httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		body, _ := io.ReadAll(response.Result().Body)
		t.Fatalf("admin user creation failed: %d %s", response.Code, body)
	}
}
