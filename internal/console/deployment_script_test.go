package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBuildStandaloneDeploymentScript(t *testing.T) {
	exported, err := BuildDeploymentScript(DeploymentRequest{
		Name: "small mysql", Mode: "standalone", Version: "8.0.46", Port: 3306,
		RootPassword: "must-not-be-exported",
		Nodes:        []DeploymentNode{{HostID: 7}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Filename != "aim-small-mysql.sh" || !exported.SecretsOmitted {
		t.Fatalf("unexpected export metadata: %+v", exported)
	}
	for _, expected := range []string{"/opt/aim/aim.sh", "--role \"$role\"", "--no-print-secrets", "require_secret AIM_ROOT_PASSWORD"} {
		if !strings.Contains(exported.Content, expected) {
			t.Fatalf("generated script does not contain %q", expected)
		}
	}
	if strings.Contains(exported.Content, "must-not-be-exported") {
		t.Fatal("generated script leaked the submitted root password")
	}
	assertValidBash(t, exported.Content)
}

func TestBuildMGRRouterDeploymentScript(t *testing.T) {
	exported, err := BuildDeploymentScript(DeploymentRequest{
		Name: "primary mgr", Mode: "mgr", Version: "8.0.46", Port: 3319, MGRPort: 33061,
		DeployRouter: true, RouterRWPort: 6460, RouterClusterName: "MGR01",
		MGRRecoveryPassword: "recovery-secret", MGRAdminPassword: "admin-secret", RootPassword: "root-secret",
		Nodes: []DeploymentNode{
			{HostID: 1, LocalIP: "10.17.0.12", RouterIP: "10.17.0.12", ServerID: 101},
			{HostID: 2, LocalIP: "10.17.0.13", RouterIP: "10.17.0.13", ServerID: 102},
			{HostID: 3, LocalIP: "10.17.0.89", RouterIP: "10.17.0.89", ServerID: 103},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"NODE_IPS=('10.17.0.12' '10.17.0.13' '10.17.0.89')",
		"--mgr-bootstrap", "--mgr-seeds '10.17.0.12:33061,10.17.0.13:33061,10.17.0.89:33061'",
		"/opt/aim/router.sh", "--rw-port 6460", "args+=(--adopt)", "install|router",
	} {
		if !strings.Contains(exported.Content, expected) {
			t.Fatalf("generated MGR script does not contain %q", expected)
		}
	}
	if !regexp.MustCompile(`--mgr-group-name '[0-9a-f-]{36}'`).MatchString(exported.Content) {
		t.Fatal("generated MGR script did not persist an automatically generated group UUID")
	}
	for _, secret := range []string{"recovery-secret", "admin-secret", "root-secret"} {
		if strings.Contains(exported.Content, secret) {
			t.Fatalf("generated script leaked %s", secret)
		}
	}
	assertValidBash(t, exported.Content)
}

func TestBuildDeploymentScriptWithUploadedMedia(t *testing.T) {
	sha := strings.Repeat("a", 64)
	exported, err := BuildDeploymentScript(DeploymentRequest{
		Name: "offline", Mode: "standalone", Version: "8.0.46", Port: 3306, MediaID: 9,
		Nodes: []DeploymentNode{{HostID: 1}},
	}, &Media{ID: 9, Filename: "mysql-8.0.46-linux-glibc2.28-x86_64.tar.xz", Version: "8.0.46", SHA256: sha})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"AIM_ARCHIVE_PATH", sha, "--archive \"$ARCHIVE_PATH\" --no-download", "SHA-256"} {
		if !strings.Contains(exported.Content+strings.Join(exported.Instructions, "\n"), expected) {
			t.Fatalf("offline export does not contain %q", expected)
		}
	}
	assertValidBash(t, exported.Content)
}

func TestBuildReplicaScriptPromptsForSourcePassword(t *testing.T) {
	exported, err := BuildDeploymentScript(DeploymentRequest{
		Name: "replica", Mode: "replica", Version: "8.0.46", Port: 3306,
		SourceHost: "10.0.0.10", SourcePort: 3306,
		Nodes: []DeploymentNode{{HostID: 2, ServerID: 102}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(exported.Content, "require_secret AIM_SOURCE_PASSWORD") {
		t.Fatal("replica export did not prompt for the source replication password")
	}
	assertValidBash(t, exported.Content)
}

func assertValidBash(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deployment.sh")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("generated script is not valid Bash: %v\n%s\n%s", err, output, content)
	}
}
