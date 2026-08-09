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

func TestStoppedAIMInstanceIsAResumeCandidate(t *testing.T) {
	facts := executor.HostFacts{Ports: map[int]string{3319: "available"}, AIMInstances: map[int]string{3319: "configured"}}
	if !hasResumableAIMInstance(facts, 3319) {
		t.Fatal("stopped AIM instance was not selected for bounded resume")
	}
	if hasResumableAIMInstance(executor.HostFacts{AIMInstances: map[int]string{}}, 3319) {
		t.Fatal("missing AIM metadata was selected for resume")
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

func TestMGRRouterDeploymentValidation(t *testing.T) {
	input := DeploymentRequest{
		Name: "test-mgr-router", Mode: "mgr", Version: "8.0.46", Port: 3319, MGRPort: 33061,
		DeployRouter: true, RouterRWPort: 6460, RouterClusterName: "MGR01",
		Nodes: []DeploymentNode{
			{HostID: 1, LocalIP: "10.17.0.12", ServerID: 101},
			{HostID: 2, LocalIP: "10.17.0.13", ServerID: 102},
			{HostID: 3, LocalIP: "10.17.0.89", ServerID: 103},
		},
	}
	if err := validateDeployment(&input); err != nil {
		t.Fatalf("valid Router deployment rejected: %v", err)
	}
	if input.MGRAdminUser != "aim_cluster_admin" || input.Nodes[1].RouterIP != "10.17.0.13" {
		t.Fatal("Router defaults were not derived from the MGR topology")
	}
	input.RouterRWPort = 3318
	if err := validateDeployment(&input); err == nil {
		t.Fatal("Router listener range overlapping the SQL port was accepted")
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

func TestFailedJobAndErrorHostDeletion(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-04T00:00:00Z"
	hostResult, err := store.DB.Exec(`INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,status,created_at,updated_at) VALUES('failed-host','192.0.2.10',22,'aimops','encrypted','error',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := hostResult.LastInsertId()
	jobID := "failed-job-to-delete"
	if _, err := store.DB.Exec(`INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at,completed_at,error) VALUES(?,'deployment','failed','{}',1,?,?, 'archive invalid')`, jobID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO job_hosts(job_id,host_id,step_order,state) VALUES(?,?,0,'failed')`, jobID, hostID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO job_logs(job_id,created_at,level,phase,message) VALUES(?,?,'error','complete','failed')`, jobID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO host_locks(host_id,job_id,acquired_at) VALUES(?,?,?)`, hostID, jobID, now); err != nil {
		t.Fatal(err)
	}

	server := &Server{Store: store}
	admin := &User{ID: 1, Username: "admin", Role: "admin", Active: true}
	request := func(method, pathValue string) *http.Request {
		r := httptest.NewRequest(method, "/", nil)
		r.SetPathValue("id", pathValue)
		return r.WithContext(context.WithValue(r.Context(), userContextKey, admin))
	}

	response := httptest.NewRecorder()
	server.deleteHost(response, request(http.MethodDelete, "1"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("empty error host with terminal history could not be deleted: %d %s", response.Code, response.Body.String())
	}
	for table, query := range map[string]string{
		"jobs":       `SELECT COUNT(*) FROM jobs WHERE id='failed-job-to-delete'`,
		"job_hosts":  `SELECT COUNT(*) FROM job_hosts WHERE job_id='failed-job-to-delete'`,
		"job_logs":   `SELECT COUNT(*) FROM job_logs WHERE job_id='failed-job-to-delete'`,
		"host_locks": `SELECT COUNT(*) FROM host_locks WHERE job_id='failed-job-to-delete'`,
	} {
		var count int
		expected := 0
		if table == "jobs" || table == "job_logs" {
			expected = 1
		}
		if err := store.DB.QueryRow(query).Scan(&count); err != nil || count != expected {
			t.Fatalf("unexpected %s count after host deletion: count=%d expected=%d err=%v", table, count, expected, err)
		}
	}
	var hostCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=?`, hostID).Scan(&hostCount); err != nil || hostCount != 0 {
		t.Fatalf("host was not deleted: count=%d err=%v", hostCount, err)
	}
	var auditCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='host_delete'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("deletions were not audited: count=%d err=%v", auditCount, err)
	}
}

func TestHostUpdateResetsPinnedIdentity(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-07T00:00:00Z"
	result, err := store.DB.Exec(`INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,host_key_fingerprint,facts_json,status,created_at,updated_at) VALUES('old-host','old.example.test',22,'aimops','old-cipher','SHA256:old','{"architecture":"amd64"}','confirmed',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := result.LastInsertId()
	server := &Server{Store: store, Secrets: testSecretBox(t)}
	admin := &User{ID: 1, Username: "admin", Role: "admin", Active: true}
	body := bytes.NewBufferString(`{"name":"new-host","address":"new.example.test","ssh_port":2222,"ssh_user":"aimops","private_key":""}`)
	request := httptest.NewRequest(http.MethodPatch, "/", body)
	request.SetPathValue("id", "1")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, admin))
	response := httptest.NewRecorder()
	server.updateHost(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("host update failed: %d %s", response.Code, response.Body.String())
	}
	var name, address, user, cipher, fingerprint, facts, status string
	var port int
	if err := store.DB.QueryRow(`SELECT name,address,ssh_port,ssh_user,private_key_cipher,host_key_fingerprint,facts_json,status FROM hosts WHERE id=?`, hostID).
		Scan(&name, &address, &port, &user, &cipher, &fingerprint, &facts, &status); err != nil {
		t.Fatal(err)
	}
	if name != "new-host" || address != "new.example.test" || port != 2222 || user != "aimops" {
		t.Fatalf("host connection fields were not updated: %s %s %d %s", name, address, port, user)
	}
	if cipher != "old-cipher" {
		t.Fatal("blank private key unexpectedly replaced the stored key")
	}
	if fingerprint != "" || facts != "{}" || status != "pending" {
		t.Fatalf("changed connection retained stale trust or facts: fingerprint=%q facts=%q status=%q", fingerprint, facts, status)
	}
	var auditCount int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='host_update' AND object_id=?`, hostID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("host update was not audited: count=%d err=%v", auditCount, err)
	}
}

func TestConfirmedHostCanBeDeletedBeforeFirstProbe(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-07T00:00:00Z"
	result, err := store.DB.Exec(`INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,host_key_fingerprint,status,created_at,updated_at) VALUES('unreachable','unreachable.example.test',22,'aimops','encrypted','SHA256:test','confirmed',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := result.LastInsertId()
	server := &Server{Store: store}
	admin := &User{ID: 1, Username: "admin", Role: "admin", Active: true}
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.SetPathValue("id", "1")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, admin))
	response := httptest.NewRecorder()
	server.deleteHost(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("confirmed unreachable host could not be deleted: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=?`, hostID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("confirmed host still exists: count=%d err=%v", count, err)
	}
}

func TestDeletionRejectsUnsafeStates(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-04T00:00:00Z"
	hostResult, err := store.DB.Exec(`INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,status,created_at,updated_at) VALUES('online-host','192.0.2.11',22,'aimops','encrypted','online',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := hostResult.LastInsertId()
	if _, err := store.DB.Exec(`INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at) VALUES('running-job','deployment','running','{}',1,?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO job_hosts(job_id,host_id,step_order,state) VALUES('running-job',?,0,'running')`, hostID); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store}
	admin := &User{ID: 1, Username: "admin", Role: "admin", Active: true}
	request := func(method, id string) *http.Request {
		r := httptest.NewRequest(method, "/", nil)
		r.SetPathValue("id", id)
		return r.WithContext(context.WithValue(r.Context(), userContextKey, admin))
	}

	response := httptest.NewRecorder()
	server.deleteHost(response, request(http.MethodDelete, "1"))
	if response.Code != http.StatusConflict {
		t.Fatalf("online host deletion was allowed: %d", response.Code)
	}
	response = httptest.NewRecorder()
	server.deleteJob(response, request(http.MethodDelete, "running-job"))
	if response.Code != http.StatusConflict {
		t.Fatalf("running job deletion was allowed: %d", response.Code)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=?`, hostID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("protected host changed: count=%d err=%v", count, err)
	}
}

func TestFailedDeploymentCleanupRejectsDeletedHostsBeforeCreatingJob(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	deployment := DeploymentRequest{
		Name:    "deleted-host-cleanup",
		Mode:    "single",
		Version: "8.0.46",
		Port:    3306,
		Nodes:   []DeploymentNode{{HostID: 99}},
	}
	payload, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at) VALUES('failed-deleted-host','deployment','failed',?,1,'2026-08-09T00:00:00Z')`, payload); err != nil {
		t.Fatal(err)
	}
	manager := &JobManager{Store: store}
	_, _, err = manager.CreateFailedDeploymentCleanup(context.Background(), &User{ID: 1, Username: "admin", Role: "admin", Active: true}, "127.0.0.1", "failed-deleted-host", FailedDeploymentCleanupInput{DryRun: true})
	if err == nil || !strings.Contains(err.Error(), "主机 99 已被删除") {
		t.Fatalf("deleted host did not produce an actionable cleanup error: %v", err)
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed cleanup left a partial job: count=%d err=%v", count, err)
	}
}

func TestHostDeletionRequiresFailedDeploymentResolution(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "admin", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-09T00:00:00Z"
	hostResult, err := store.DB.Exec(`INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,status,created_at,updated_at) VALUES('failed-host','192.0.2.99',22,'aimops','encrypted','error',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	hostID, _ := hostResult.LastInsertId()
	deployment := DeploymentRequest{Name: "failed-host", Mode: "single", Version: "8.0.46", Port: 3306, Nodes: []DeploymentNode{{HostID: hostID}}}
	payload, _ := json.Marshal(deployment)
	if _, err := store.DB.Exec(`INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at) VALUES('failed-host-job','deployment','failed',?,1,?)`, payload, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO job_hosts(job_id,host_id,step_order,state) VALUES('failed-host-job',?,0,'failed')`, hostID); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store}
	request := httptest.NewRequest(http.MethodDelete, "/", nil)
	request.SetPathValue("id", "1")
	request = request.WithContext(context.WithValue(request.Context(), userContextKey, &User{ID: 1, Username: "admin", Role: "admin", Active: true}))
	response := httptest.NewRecorder()
	server.deleteHost(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "先清理失败安装") {
		t.Fatalf("host with unresolved failed deployment was not protected: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM hosts WHERE id=?`, hostID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("protected host was deleted: count=%d err=%v", count, err)
	}
}

func TestOIDCUserProvisioningAndRoleSync(t *testing.T) {
	store := testStore(t)
	if _, err := store.BootstrapAdmin(context.Background(), "existing", "very-strong-admin-password"); err != nil {
		t.Fatal(err)
	}
	user, err := store.UpsertOIDCUser(context.Background(), "lazycat-subject-1", "existing", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username == "existing" || user.Role != "viewer" || !user.Active {
		t.Fatalf("unexpected provisioned OIDC user: %+v", user)
	}
	updated, err := store.UpsertOIDCUser(context.Background(), "lazycat-subject-1", "ignored-new-name", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != user.ID || updated.Username != user.Username || updated.Role != "admin" {
		t.Fatalf("OIDC user was not stably updated: %+v", updated)
	}
	var provider, subject string
	if err := store.DB.QueryRow(`SELECT auth_provider,oidc_subject FROM users WHERE id=?`, user.ID).Scan(&provider, &subject); err != nil {
		t.Fatal(err)
	}
	if provider != "oidc" || subject != "lazycat-subject-1" {
		t.Fatalf("unexpected OIDC identity: provider=%q subject=%q", provider, subject)
	}
}

func TestFilePickerContentSecurityPolicy(t *testing.T) {
	if got := normalizeFilePickerOrigin("https://file.landan.heiyu.space"); got != "https://file.landan.heiyu.space" {
		t.Fatalf("unexpected normalized picker origin: %q", got)
	}
	for _, invalid := range []string{"http://file.landan.heiyu.space", "https://file.landan.heiyu.space/path", "https://file.landan.heiyu.space; frame-src *"} {
		if got := normalizeFilePickerOrigin(invalid); got != "" {
			t.Fatalf("unsafe picker origin was accepted: %q", got)
		}
	}
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "https://file.landan.heiyu.space")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := response.Header().Get("Content-Security-Policy")
	for _, expected := range []string{"style-src 'self' 'unsafe-inline'", "frame-src 'self' https://file.landan.heiyu.space"} {
		if !strings.Contains(policy, expected) {
			t.Fatalf("CSP is missing %q: %s", expected, policy)
		}
	}
}

func TestBackupPlanValidation(t *testing.T) {
	if err := validateBackupSchedule("0 2 * * *"); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	if err := validateBackupSchedule("not-a-cron"); err == nil {
		t.Fatal("invalid schedule accepted")
	}
	if !validBackupDatabase("application_db") || validBackupDatabase("-unsafe") || validBackupDatabase("line\nfeed") {
		t.Fatal("database name validation failed")
	}
}
