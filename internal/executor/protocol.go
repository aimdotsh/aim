package executor

import "time"

const ProtocolVersion = 1

type Config struct {
	AimPath     string `json:"aim_path"`
	RouterPath  string `json:"router_path"`
	BaseRoot    string `json:"base_root"`
	DataRoot    string `json:"data_root"`
	LogRoot     string `json:"log_root"`
	TmpRoot     string `json:"tmp_root"`
	StagingRoot string `json:"staging_root"`
	// The executor runs as root through sudo, while SFTP sessions use
	// aimops.  These IDs make generated backup artifacts readable by the
	// restricted account without making the whole staging tree world-readable.
	StagingUID int `json:"staging_uid,omitempty"`
	StagingGID int `json:"staging_gid,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		AimPath:     "/opt/aim/aim.sh",
		RouterPath:  "/opt/aim/router.sh",
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
	AdminUser    string   `json:"admin_user,omitempty"`
	AdminHosts   []string `json:"admin_hosts,omitempty"`
}

// Router describes one MySQL Router instance attached to an adopted
// InnoDB Cluster. RWPort is the Classic protocol read/write port; Router also
// creates Classic RO, X RW and X RO listeners at RWPort+1 through RWPort+3.
type Router struct {
	ClusterName string `json:"cluster_name,omitempty"`
	BindAddress string `json:"bind_address,omitempty"`
	RWPort      int    `json:"rw_port,omitempty"`
	Adopt       bool   `json:"adopt,omitempty"`
}

type Secrets struct {
	RootPassword        string `json:"root_password,omitempty"`
	ReplicationPassword string `json:"replication_password,omitempty"`
	SourcePassword      string `json:"source_password,omitempty"`
	MGRRecoveryPassword string `json:"mgr_recovery_password,omitempty"`
	MGRAdminPassword    string `json:"mgr_admin_password,omitempty"`
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
	Router      Router      `json:"router,omitempty"`
	Secrets     Secrets     `json:"secrets,omitempty"`
	DryRun      bool        `json:"dry_run,omitempty"`
	Confirm     bool        `json:"confirm,omitempty"`
	ProbePorts  []int       `json:"probe_ports,omitempty"`
	Backup      *BackupSpec `json:"backup,omitempty"`
}

// BackupSpec deliberately contains only the logical dump selection.  The
// executor derives the output path from RequestID inside its private staging
// root; callers cannot make it write arbitrary host paths.
type BackupSpec struct {
	AllDatabases bool     `json:"all_databases,omitempty"`
	Databases    []string `json:"databases,omitempty"`
}

type BackupResult struct {
	RemotePath string `json:"remote_path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	DumpClient string `json:"dump_client"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// Metrics is intentionally a flat, JSON-friendly snapshot.  It covers host
// capacity, process health, MySQL connection pressure, throughput and common
// replication/MGR indicators without requiring a long-running agent.
type Metrics struct {
	CollectedAt               string  `json:"collected_at"`
	Hostname                  string  `json:"hostname"`
	CPUPercent                float64 `json:"cpu_percent"`
	Load1                     float64 `json:"load1"`
	Load5                     float64 `json:"load5"`
	Load15                    float64 `json:"load15"`
	MemoryTotalMB             uint64  `json:"memory_total_mb"`
	MemoryUsedMB              uint64  `json:"memory_used_mb"`
	MemoryAvailableMB         uint64  `json:"memory_available_mb"`
	SwapTotalMB               uint64  `json:"swap_total_mb"`
	SwapUsedMB                uint64  `json:"swap_used_mb"`
	DiskFreeMB                uint64  `json:"disk_free_mb"`
	MySQLUp                   bool    `json:"mysql_up"`
	MySQLError                string  `json:"mysql_error,omitempty"`
	MySQLUptime               int64   `json:"mysql_uptime"`
	ThreadsConnected          int64   `json:"threads_connected"`
	ThreadsRunning            int64   `json:"threads_running"`
	MaxUsedConnections        int64   `json:"max_used_connections"`
	MaxConnections            int64   `json:"max_connections"`
	Connections               int64   `json:"connections"`
	AbortedConnects           int64   `json:"aborted_connects"`
	ThreadsCreated            int64   `json:"threads_created"`
	Queries                   int64   `json:"queries"`
	Questions                 int64   `json:"questions"`
	SlowQueries               int64   `json:"slow_queries"`
	BytesReceived             int64   `json:"bytes_received"`
	BytesSent                 int64   `json:"bytes_sent"`
	OpenTables                int64   `json:"open_tables"`
	TableLocksWaited          int64   `json:"table_locks_waited"`
	InnodbBufferPoolDataPages int64   `json:"innodb_buffer_pool_data_pages"`
	InnodbBufferPoolFreePages int64   `json:"innodb_buffer_pool_free_pages"`
	ReplicationIO             string  `json:"replication_io,omitempty"`
	ReplicationSQL            string  `json:"replication_sql,omitempty"`
	ReplicationLag            int64   `json:"replication_lag,omitempty"`
	MGRState                  string  `json:"mgr_state,omitempty"`
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
	Protocol  int           `json:"protocol"`
	RequestID string        `json:"request_id,omitempty"`
	Time      time.Time     `json:"time"`
	Level     string        `json:"level"`
	Phase     string        `json:"phase"`
	Message   string        `json:"message,omitempty"`
	Facts     *HostFacts    `json:"facts,omitempty"`
	ExitCode  *int          `json:"exit_code,omitempty"`
	OK        *bool         `json:"ok,omitempty"`
	Backup    *BackupResult `json:"backup,omitempty"`
	Metrics   *Metrics      `json:"metrics,omitempty"`
}
