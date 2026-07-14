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
