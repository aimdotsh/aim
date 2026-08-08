package console

import "time"

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Active   bool   `json:"active"`
}

type Host struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	Address            string         `json:"address"`
	SSHPort            int            `json:"ssh_port"`
	SSHUser            string         `json:"ssh_user"`
	PrivateKeyCipher   string         `json:"-"`
	HostKeyFingerprint string         `json:"host_key_fingerprint"`
	Facts              map[string]any `json:"facts"`
	Status             string         `json:"status"`
	LastError          string         `json:"last_error"`
	LastSeenAt         *time.Time     `json:"last_seen_at,omitempty"`
}

type Media struct {
	ID           int64  `json:"id"`
	Filename     string `json:"filename"`
	Path         string `json:"-"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Version      string `json:"version"`
	Glibc        string `json:"glibc"`
	Architecture string `json:"architecture"`
	Minimal      bool   `json:"minimal"`
	Format       string `json:"format"`
	CreatedAt    string `json:"created_at"`
}

type Job struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	State       string `json:"state"`
	PayloadJSON string `json:"payload_json,omitempty"`
	ResultJSON  string `json:"result_json,omitempty"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

type BackupPlan struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	InstanceID     int64    `json:"instance_id"`
	InstanceName   string   `json:"instance_name,omitempty"`
	HostName       string   `json:"host_name,omitempty"`
	Address        string   `json:"address,omitempty"`
	Version        string   `json:"version"`
	Port           int      `json:"port"`
	Schedule       string   `json:"schedule"`
	AllDatabases   bool     `json:"all_databases"`
	DatabasesJSON  string   `json:"databases_json,omitempty"`
	Databases      []string `json:"databases,omitempty"`
	RetentionDays  int      `json:"retention_days"`
	RetentionCount int      `json:"retention_count"`
	Enabled        bool     `json:"enabled"`
	OwnerID        int64    `json:"owner_id"`
	OwnerUsername  string   `json:"owner_username,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type BackupRun struct {
	ID            string `json:"id"`
	PlanID        int64  `json:"plan_id"`
	PlanName      string `json:"plan_name,omitempty"`
	InstanceID    int64  `json:"instance_id"`
	OwnerID       int64  `json:"owner_id"`
	OwnerUsername string `json:"owner_username,omitempty"`
	Status        string `json:"status"`
	FileName      string `json:"file_name,omitempty"`
	FilePath      string `json:"-"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256,omitempty"`
	Message       string `json:"message,omitempty"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
}

type MonitorSample struct {
	ID          int64  `json:"id"`
	InstanceID  int64  `json:"instance_id"`
	CollectedAt string `json:"collected_at"`
	MetricsJSON string `json:"metrics_json"`
}
