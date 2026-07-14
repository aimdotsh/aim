package executor

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	versionPattern   = regexp.MustCompile(`^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$`)
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)
	userPattern      = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	uuidPattern      = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	archivePattern   = regexp.MustCompile(`^mysql-((?:5\.6|5\.7|8\.0|8\.4)\.[0-9]+)-linux-glibc([0-9]+\.[0-9]+)-(x86_64|aarch64|i686)(-minimal)?\.(tar\.xz|tar\.gz|tgz|tar)$`)
)

type archiveMetadata struct {
	version, glibc, architecture, format string
	minimal                              bool
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, validateConfig(cfg)
	}
	if err != nil {
		return Config{}, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode executor config: %w", err)
	}
	return cfg, validateConfig(cfg)
}

func validateConfig(cfg Config) error {
	if !filepath.IsAbs(cfg.AimPath) || filepath.Clean(cfg.AimPath) == "/" {
		return errors.New("aim_path must be an absolute non-root path")
	}
	for name, path := range map[string]string{
		"base_root": cfg.BaseRoot, "data_root": cfg.DataRoot, "log_root": cfg.LogRoot,
		"tmp_root": cfg.TmpRoot, "staging_root": cfg.StagingRoot,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) == "/" || filepath.Clean(path) != path {
			return fmt.Errorf("%s must be a clean absolute non-root path", name)
		}
	}
	return nil
}

func DecodeRequest(r io.Reader) (Request, error) {
	var req Request
	decoder := json.NewDecoder(io.LimitReader(r, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("decode request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("only one JSON request is allowed")
	}
	return req, nil
}

func ValidateRequest(req Request, cfg Config) error {
	if req.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol: %d", req.Protocol)
	}
	if !requestIDPattern.MatchString(req.RequestID) {
		return errors.New("invalid request_id")
	}
	if req.Action == "probe" {
		for _, port := range req.ProbePorts {
			if port < 1 || port > 65535 {
				return fmt.Errorf("invalid probe port: %d", port)
			}
		}
		return nil
	}
	if req.Action != "install" && req.Action != "reinitialize" && req.Action != "uninstall" &&
		req.Action != "start" && req.Action != "stop" && req.Action != "status" {
		return errors.New("unsupported action")
	}
	if !versionPattern.MatchString(req.Version) {
		return errors.New("invalid MySQL version")
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("invalid MySQL port")
	}
	if (req.Action == "reinitialize" || req.Action == "uninstall") && !req.DryRun && !req.Confirm {
		return errors.New("destructive action requires dry_run or confirm")
	}
	if req.Action == "install" || req.Action == "reinitialize" {
		if req.Role == "" {
			req.Role = "standalone"
		}
		if req.Role != "standalone" && req.Role != "source" && req.Role != "replica" && req.Role != "mgr" {
			return errors.New("invalid role")
		}
		if req.BindAddress != "" && net.ParseIP(req.BindAddress) == nil {
			return errors.New("bind_address must be an IP address")
		}
		if req.Role == "source" && req.Replication.ReplicaHost == "" {
			return errors.New("source role requires replica_host")
		}
		if req.Role == "replica" {
			if net.ParseIP(req.Replication.SourceHost) == nil || req.Replication.SourcePort < 1 || req.Replication.SourcePort > 65535 {
				return errors.New("replica role requires a valid source host and port")
			}
			if req.Secrets.SourcePassword == "" {
				return errors.New("replica role requires source password")
			}
		}
		if req.Role == "mgr" {
			if !strings.HasPrefix(req.Version, "8.0.") || !versionAtLeast(req.Version, 8, 0, 23) || req.ServerID == 0 || net.ParseIP(req.MGR.LocalAddress) == nil {
				return errors.New("MGR requires MySQL 8.0, server_id and a local IP")
			}
			if req.MGR.Port < 1 || req.MGR.Port > 65535 || req.MGR.Port == req.Port || len(req.MGR.Seeds) != 3 {
				return errors.New("MGR requires three seeds and a distinct valid MGR port")
			}
			for _, seed := range req.MGR.Seeds {
				host, port, err := net.SplitHostPort(seed)
				if err != nil || net.ParseIP(host) == nil || port != strconv.Itoa(req.MGR.Port) {
					return fmt.Errorf("invalid MGR seed: %s", seed)
				}
			}
			if !uuidPattern.MatchString(req.MGR.GroupName) || req.MGR.Allowlist == "" || req.Secrets.MGRRecoveryPassword == "" {
				return errors.New("MGR group name, allowlist and recovery password are required")
			}
			if req.MGR.RecoveryUser != "" && !userPattern.MatchString(req.MGR.RecoveryUser) {
				return errors.New("invalid MGR recovery user")
			}
		}
	}
	if req.Archive != nil {
		if err := validateArchiveMetadata(*req.Archive, req.Version); err != nil {
			return err
		}
		if _, err := verifiedArchivePath(req, cfg); err != nil {
			return err
		}
	}
	return nil
}

func versionAtLeast(value string, major, minor, patch int) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	actual := make([]int, 3)
	for index, part := range parts {
		actual[index], _ = strconv.Atoi(part)
	}
	if actual[0] != major {
		return actual[0] > major
	}
	if actual[1] != minor {
		return actual[1] > minor
	}
	return actual[2] >= patch
}

func validateArchiveMetadata(archive Archive, requestedVersion string) error {
	if archive.Name != filepath.Base(archive.Name) || strings.HasPrefix(archive.Name, "mysql-test-") {
		return errors.New("invalid server archive name")
	}
	metadata, err := parseArchiveName(archive.Name)
	if err != nil {
		return err
	}
	if archive.Size <= 0 || archive.Version == "" || archive.Glibc == "" || archive.Architecture == "" {
		return errors.New("complete archive metadata is required")
	}
	if archive.Version != metadata.version || archive.Version != requestedVersion || archive.Glibc != metadata.glibc ||
		archive.Architecture != metadata.architecture || archive.Minimal != metadata.minimal {
		return errors.New("archive metadata does not match its filename or request")
	}
	if len(archive.SHA256) != 64 {
		return errors.New("archive SHA-256 is required")
	}
	if _, err := hex.DecodeString(archive.SHA256); err != nil {
		return errors.New("invalid archive SHA-256")
	}
	return nil
}

func parseArchiveName(name string) (archiveMetadata, error) {
	matches := archivePattern.FindStringSubmatch(name)
	if matches == nil {
		return archiveMetadata{}, errors.New("unsupported archive filename or suffix")
	}
	return archiveMetadata{version: matches[1], glibc: matches[2], architecture: matches[3], minimal: matches[4] != "", format: matches[5]}, nil
}

func verifiedArchivePath(req Request, cfg Config) (string, error) {
	realPath, err := boundedArchivePath(req, cfg)
	if err != nil {
		return "", err
	}
	f, err := os.Open(realPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), req.Archive.SHA256) {
		return "", errors.New("archive SHA-256 mismatch")
	}
	return realPath, nil
}

func boundedArchivePath(req Request, cfg Config) (string, error) {
	root := filepath.Join(cfg.StagingRoot, req.RequestID)
	path := filepath.Join(root, req.Archive.Name)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("staging directory is unavailable: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("staged archive is unavailable: %w", err)
	}
	if realPath == realRoot || !strings.HasPrefix(realPath, realRoot+string(os.PathSeparator)) {
		return "", errors.New("archive escaped staging directory")
	}
	info, err := os.Stat(realPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("archive must be a regular file")
	}
	if info.Size() != req.Archive.Size {
		return "", errors.New("archive size mismatch")
	}
	metadata, _ := parseArchiveName(req.Archive.Name)
	if err := verifyArchiveMagic(realPath, metadata.format); err != nil {
		return "", err
	}
	return realPath, nil
}

func verifyArchiveMagic(path, format string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, 512)
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	header = header[:n]
	valid := false
	switch format {
	case "tar.xz":
		valid = len(header) >= 6 && string(header[:6]) == "\xfd7zXZ\x00"
	case "tar.gz", "tgz":
		valid = len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b
	case "tar":
		valid = len(header) >= 262 && string(header[257:262]) == "ustar"
	}
	if !valid {
		return fmt.Errorf("archive content does not match %s format", format)
	}
	return nil
}

func validateArchiveHostCompatibility(archive Archive, facts HostFacts) error {
	arch := facts.Architecture
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	} else if arch == "386" {
		arch = "i686"
	}
	if arch != archive.Architecture {
		return fmt.Errorf("archive architecture %s is incompatible with host %s", archive.Architecture, facts.Architecture)
	}
	if facts.Glibc == "" || compareNumericVersion(facts.Glibc, archive.Glibc) < 0 {
		return fmt.Errorf("archive requires glibc %s but host has %s", archive.Glibc, facts.Glibc)
	}
	return nil
}

func compareNumericVersion(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for len(left) < len(right) {
		left = append(left, "0")
	}
	for len(right) < len(left) {
		right = append(right, "0")
	}
	for index := range left {
		lv, _ := strconv.Atoi(left[index])
		rv, _ := strconv.Atoi(right[index])
		if lv < rv {
			return -1
		}
		if lv > rv {
			return 1
		}
	}
	return 0
}

func BuildCommand(req Request, cfg Config) ([]string, []string, error) {
	if err := ValidateRequest(req, cfg); err != nil {
		return nil, nil, err
	}
	if req.Action == "probe" {
		return nil, nil, nil
	}
	args := []string{"-v", req.Version, "-p", strconv.Itoa(req.Port),
		"--base-root", cfg.BaseRoot, "--data-root", cfg.DataRoot,
		"--log-root", cfg.LogRoot, "--tmp-root", cfg.TmpRoot,
		"--machine-readable", "--no-print-secrets"}
	switch req.Action {
	case "start", "stop", "status":
		args = append(args, "--"+req.Action)
	case "uninstall":
		args = append(args, "--uninstall")
	case "reinitialize":
		args = append(args, "--reinitialize")
	}
	if req.Action == "install" || req.Action == "reinitialize" {
		role := req.Role
		if role == "" {
			role = "standalone"
		}
		args = append(args, "--role", role)
		if req.BindAddress != "" {
			args = append(args, "--bind-address", req.BindAddress)
		}
		if req.ServerID != 0 {
			args = append(args, "--server-id", strconv.FormatUint(uint64(req.ServerID), 10))
		}
		if req.GTID != nil && !*req.GTID {
			args = append(args, "--no-gtid")
		}
		if role == "source" {
			args = append(args, "--replica-host", req.Replication.ReplicaHost)
			if req.Replication.User != "" {
				args = append(args, "--repl-user", req.Replication.User)
			}
		}
		if role == "replica" {
			args = append(args, "--source-host", req.Replication.SourceHost, "--source-port", strconv.Itoa(req.Replication.SourcePort))
			if req.Replication.User != "" {
				args = append(args, "--source-user", req.Replication.User)
			}
		}
		if role == "mgr" {
			args = append(args, "--mgr-local-address", req.MGR.LocalAddress, "--mgr-port", strconv.Itoa(req.MGR.Port),
				"--mgr-seeds", strings.Join(req.MGR.Seeds, ","), "--mgr-group-name", req.MGR.GroupName,
				"--mgr-allowlist", req.MGR.Allowlist)
			if req.MGR.Bootstrap {
				args = append(args, "--mgr-bootstrap")
			}
			if req.MGR.RecoveryUser != "" {
				args = append(args, "--mgr-recovery-user", req.MGR.RecoveryUser)
			}
		}
	}
	if req.Archive != nil {
		path, err := boundedArchivePath(req, cfg)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, "--archive", path, "--no-download")
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Confirm {
		args = append(args, "--yes")
	}
	env := []string{
		"AIM_ROOT_PASSWORD=" + req.Secrets.RootPassword,
		"AIM_REPL_PASSWORD=" + req.Secrets.ReplicationPassword,
		"AIM_SOURCE_PASSWORD=" + req.Secrets.SourcePassword,
		"AIM_MGR_RECOVERY_PASSWORD=" + req.Secrets.MGRRecoveryPassword,
	}
	return args, env, nil
}

type eventWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (w *eventWriter) write(event Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(event)
}

func Execute(ctx context.Context, req Request, cfg Config, output io.Writer) error {
	w := &eventWriter{enc: json.NewEncoder(output)}
	var args, secretEnv []string
	var err error
	if req.Action == "probe" {
		err = ValidateRequest(req, cfg)
	} else {
		args, secretEnv, err = BuildCommand(req, cfg)
	}
	if err != nil {
		ok, code := false, 2
		w.write(Event{Protocol: ProtocolVersion, RequestID: req.RequestID, Time: time.Now().UTC(), Level: "error", Phase: "validate", Message: err.Error(), ExitCode: &code, OK: &ok})
		return err
	}
	if req.Action == "probe" {
		facts, err := ProbeHost(req.ProbePorts, cfg.DataRoot)
		if err != nil {
			return err
		}
		ok, code := true, 0
		w.write(Event{Protocol: ProtocolVersion, RequestID: req.RequestID, Time: time.Now().UTC(), Level: "info", Phase: "probe", Facts: &facts, ExitCode: &code, OK: &ok})
		return nil
	}
	if req.Archive != nil {
		facts, probeErr := ProbeHost(nil, cfg.DataRoot)
		if probeErr != nil {
			return probeErr
		}
		if err := validateArchiveHostCompatibility(*req.Archive, facts); err != nil {
			ok, code := false, 2
			w.write(Event{Protocol: ProtocolVersion, RequestID: req.RequestID, Time: time.Now().UTC(), Level: "error", Phase: "validate", Message: err.Error(), ExitCode: &code, OK: &ok})
			return err
		}
	}
	cmd := exec.CommandContext(ctx, cfg.AimPath, args...)
	cmd.Env = append(os.Environ(), secretEnv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	redact := newRedactor(req.Secrets)
	var streams sync.WaitGroup
	stream := func(level string, reader io.Reader) {
		defer streams.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			w.write(Event{Protocol: ProtocolVersion, RequestID: req.RequestID, Time: time.Now().UTC(), Level: level, Phase: "aim", Message: redact(scanner.Text())})
		}
	}
	streams.Add(2)
	go stream("info", stdout)
	go stream("warning", stderr)
	streams.Wait()
	err = cmd.Wait()
	code := 0
	ok := err == nil
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	level := "info"
	if !ok {
		level = "error"
	}
	w.write(Event{Protocol: ProtocolVersion, RequestID: req.RequestID, Time: time.Now().UTC(), Level: level, Phase: "complete", ExitCode: &code, OK: &ok})
	return err
}

func newRedactor(secrets Secrets) func(string) string {
	values := []string{secrets.RootPassword, secrets.ReplicationPassword, secrets.SourcePassword, secrets.MGRRecoveryPassword}
	return func(value string) string {
		for _, secret := range values {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[REDACTED]")
			}
		}
		return value
	}
}

func ProbeHost(ports []int, dataRoot string) (HostFacts, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return HostFacts{}, err
	}
	facts := HostFacts{Hostname: hostname, Architecture: runtime.GOARCH, CPUs: runtime.NumCPU(), Ports: map[int]string{}}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				facts.IPv4 = append(facts.IPv4, ip.String())
			}
		}
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			value = strings.Trim(value, `"`)
			switch key {
			case "ID":
				facts.OSID = value
			case "PRETTY_NAME":
				facts.OSName = value
			}
		}
	}
	if output, err := exec.Command("ldd", "--version").CombinedOutput(); err == nil || len(output) > 0 {
		fields := strings.Fields(strings.SplitN(string(output), "\n", 2)[0])
		if len(fields) > 0 {
			facts.Glibc = fields[len(fields)-1]
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseUint(fields[1], 10, 64)
					facts.MemoryMB = kb / 1024
				}
			}
		}
	}
	var stat syscall.Statfs_t
	probePath := dataRoot
	for {
		if err := syscall.Statfs(probePath, &stat); err == nil {
			facts.DiskFreeMB = uint64(stat.Bavail) * uint64(stat.Bsize) / 1024 / 1024
			break
		}
		parent := filepath.Dir(probePath)
		if parent == probePath {
			break
		}
		probePath = parent
	}
	for _, port := range ports {
		facts.Ports[port] = "available"
		addresses := append([]string{"127.0.0.1"}, facts.IPv4...)
		for _, address := range addresses {
			conn, err := net.DialTimeout("tcp4", net.JoinHostPort(address, strconv.Itoa(port)), 300*time.Millisecond)
			if err == nil {
				facts.Ports[port] = "listening"
				_ = conn.Close()
				break
			}
		}
	}
	return facts, nil
}
