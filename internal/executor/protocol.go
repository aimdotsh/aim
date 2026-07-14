package executor

import "time"

const ProtocolVersion = 1

type Config struct {
	AimPath     string `json:"aim_path"`
	BaseRoot    string `json:"base_root"`
	DataRoot    string `json:"data_root"`
	LogRoot     string `json:"log_root"`
	TmpRoot     string `json:"tmp_root"`
	StagingRoot string `json:"staging_root"`
}

func DefaultConfig() Config {
	return Config{
		AimPath:     "/opt/aim/aim.sh",
		BaseRoot:    "/opt/mysql",
		DataRoot:    "/data/mysql",
		LogRoot:     "/var/log/mysql",
		TmpRoot:     "/var/tmp/mysql",
		StagingRoot: "/var/lib/aim-staging",
	}
}

type Archive struct {
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Version      string `json:"version"`
	Glibc        string `json:"glibc"`
	Architecture string `json:"architecture"`
	Minimal      bool   `json:"minimal,omitempty"`
}

type Replication struct {
	ReplicaHost string `json:"replica_host,omitempty"`
	User        string `json:"user,omitempty"`
	SourceHost  string `json:"source_host,omitempty"`
	SourcePort  int    `json:"source_port,omitempty"`
}

type MGR struct {
	LocalAddress string   `json:"local_address,omitempty"`
	Port         int      `json:"port,omitempty"`
	Seeds        []string `json:"seeds,omitempty"`
	GroupName    string   `json:"group_name,omitempty"`
	Allowlist    string   `json:"allowlist,omitempty"`
	Bootstrap    bool     `json:"bootstrap,omitempty"`
	RecoveryUser string   `json:"recovery_user,omitempty"`
}

type Secrets struct {
	RootPassword        string `json:"root_password,omitempty"`
	ReplicationPassword string `json:"replication_password,omitempty"`
	SourcePassword      string `json:"source_password,omitempty"`
	MGRRecoveryPassword string `json:"mgr_recovery_password,omitempty"`
}

type Request struct {
	Protocol    int         `json:"protocol"`
	RequestID   string      `json:"request_id"`
	Action      string      `json:"action"`
	Version     string      `json:"version,omitempty"`
	Port        int         `json:"port,omitempty"`
	Role        string      `json:"role,omitempty"`
	BindAddress string      `json:"bind_address,omitempty"`
	ServerID    uint32      `json:"server_id,omitempty"`
	GTID        *bool       `json:"gtid,omitempty"`
	Archive     *Archive    `json:"archive,omitempty"`
	Replication Replication `json:"replication,omitempty"`
	MGR         MGR         `json:"mgr,omitempty"`
	Secrets     Secrets     `json:"secrets,omitempty"`
	DryRun      bool        `json:"dry_run,omitempty"`
	Confirm     bool        `json:"confirm,omitempty"`
	ProbePorts  []int       `json:"probe_ports,omitempty"`
}

type HostFacts struct {
	Hostname     string         `json:"hostname"`
	IPv4         []string       `json:"ipv4"`
	OSID         string         `json:"os_id"`
	OSName       string         `json:"os_name"`
	Architecture string         `json:"architecture"`
	Glibc        string         `json:"glibc"`
	CPUs         int            `json:"cpus"`
	MemoryMB     uint64         `json:"memory_mb"`
	DiskFreeMB   uint64         `json:"disk_free_mb"`
	Ports        map[int]string `json:"ports,omitempty"`
}

type Event struct {
	Protocol  int        `json:"protocol"`
	RequestID string     `json:"request_id,omitempty"`
	Time      time.Time  `json:"time"`
	Level     string     `json:"level"`
	Phase     string     `json:"phase"`
	Message   string     `json:"message,omitempty"`
	Facts     *HostFacts `json:"facts,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	OK        *bool      `json:"ok,omitempty"`
}
