package executor

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const metricsQuery = `SHOW GLOBAL STATUS WHERE Variable_name IN (
'Aborted_connects','Bytes_received','Bytes_sent','Connections','Innodb_buffer_pool_pages_data',
'Innodb_buffer_pool_pages_free','Max_used_connections','Open_tables','Queries','Questions',
'Slow_queries','Table_locks_waited','Threads_connected','Threads_created','Threads_running',
'Max_connections',
'Uptime')`

func errorMessage(err error) string {
	if err == nil {
		return "主机与 MySQL 指标采集完成"
	}
	return err.Error()
}

func safeDatabaseName(value string) bool {
	if value == "" || len(value) > 128 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, r := range value {
		if r == 0 || r == '\r' || r == '\n' || r == '\t' {
			return false
		}
	}
	return true
}

func mysqlBinary(cfg Config, version, name string) (string, error) {
	candidates := []string{
		filepath.Join(cfg.BaseRoot, version, "bin", name),
		filepath.Join(cfg.BaseRoot, version, "bin", strings.TrimPrefix(name, "maria")),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到 MySQL %s（期望位于 %s/%s/bin）", name, cfg.BaseRoot, version)
}

func stagingDir(req Request, cfg Config) (string, error) {
	if err := safeRequestID(req.RequestID); err != nil {
		return "", err
	}
	root := filepath.Join(cfg.StagingRoot, req.RequestID)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", fmt.Errorf("创建任务暂存目录失败: %w", err)
	}
	if cfg.StagingUID > 0 && cfg.StagingGID > 0 {
		if err := os.Chown(root, cfg.StagingUID, cfg.StagingGID); err != nil {
			return "", fmt.Errorf("设置任务暂存目录权限失败: %w", err)
		}
	}
	if err := os.Chmod(root, 0o750); err != nil {
		return "", fmt.Errorf("设置任务暂存目录模式失败: %w", err)
	}
	return root, nil
}

func makeStagedFileReadableBySSH(path string, cfg Config) error {
	if cfg.StagingUID > 0 && cfg.StagingGID > 0 {
		if err := os.Chown(path, cfg.StagingUID, cfg.StagingGID); err != nil {
			return err
		}
	}
	return os.Chmod(path, 0o600)
}

func safeRequestID(value string) error {
	if !requestIDPattern.MatchString(value) || filepath.Base(value) != value {
		return errors.New("invalid request_id")
	}
	return nil
}

func optionFile(dir, socket, password string) (string, error) {
	file, err := os.OpenFile(filepath.Join(dir, ".aim-client.cnf"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := fmt.Fprintf(file, "[client]\nuser=root\npassword=\"%s\"\nprotocol=socket\nsocket=\"%s\"\n", iniQuote(password), iniQuote(socket))
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	return path, nil
}

func iniQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	return value
}

func ExecuteBackup(ctx context.Context, req Request, cfg Config) (BackupResult, error) {
	if req.Backup == nil {
		return BackupResult{}, errors.New("backup selection is required")
	}
	dir, err := stagingDir(req, cfg)
	if err != nil {
		return BackupResult{}, err
	}
	socket := filepath.Join(cfg.DataRoot, strconv.Itoa(req.Port), "mysql.sock")
	dump, err := mysqlBinary(cfg, req.Version, "mysqldump")
	if err != nil {
		if alternative, alternativeErr := mysqlBinary(cfg, req.Version, "mariadb-dump"); alternativeErr == nil {
			dump = alternative
		} else {
			return BackupResult{}, err
		}
	}
	optionPath, err := optionFile(dir, socket, req.Secrets.RootPassword)
	if err != nil {
		return BackupResult{}, fmt.Errorf("创建临时凭据文件失败: %w", err)
	}
	defer os.Remove(optionPath)
	outputPath := filepath.Join(dir, "backup.sql.gz")
	partialPath := outputPath + ".partial"
	_ = os.Remove(outputPath)
	_ = os.Remove(partialPath)
	args := []string{
		"--defaults-extra-file=" + optionPath,
		"--protocol=socket", "--socket=" + socket,
		"--single-transaction", "--quick", "--skip-lock-tables",
		"--routines", "--events", "--triggers", "--hex-blob",
		"--default-character-set=utf8mb4",
	}
	if req.Backup.AllDatabases {
		args = append(args, "--all-databases")
	} else {
		args = append(args, "--databases")
		args = append(args, req.Backup.Databases...)
	}
	started := time.Now().UTC()
	command := exec.CommandContext(ctx, dump, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return BackupResult{}, err
	}
	var stderr cappedBuffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return BackupResult{}, fmt.Errorf("启动 mysqldump 失败: %w", err)
	}
	output, err := os.OpenFile(partialPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return BackupResult{}, err
	}
	hash := sha256.New()
	gz, gzipErr := gzip.NewWriterLevel(io.MultiWriter(output, hash), gzip.BestSpeed)
	if gzipErr == nil {
		_, gzipErr = io.Copy(gz, stdout)
	}
	if closeErr := gz.Close(); gzipErr == nil {
		gzipErr = closeErr
	}
	if closeErr := output.Close(); gzipErr == nil {
		gzipErr = closeErr
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		_ = os.Remove(partialPath)
		return BackupResult{}, ctx.Err()
	}
	if gzipErr != nil || waitErr != nil {
		_ = os.Remove(partialPath)
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			if gzipErr != nil {
				message = gzipErr.Error()
			} else {
				message = waitErr.Error()
			}
		}
		return BackupResult{}, fmt.Errorf("mysqldump 失败: %s", sanitizeRemoteError(message))
	}
	if err := os.Rename(partialPath, outputPath); err != nil {
		_ = os.Remove(partialPath)
		return BackupResult{}, err
	}
	if err := makeStagedFileReadableBySSH(outputPath, cfg); err != nil {
		_ = os.Remove(outputPath)
		return BackupResult{}, fmt.Errorf("设置备份文件权限失败: %w", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return BackupResult{}, err
	}
	finished := time.Now().UTC()
	return BackupResult{
		RemotePath: outputPath,
		Size:       info.Size(),
		SHA256:     hex.EncodeToString(hash.Sum(nil)),
		DumpClient: filepath.Base(dump),
		StartedAt:  started.Format(time.RFC3339Nano),
		FinishedAt: finished.Format(time.RFC3339Nano),
	}, nil
}

func sanitizeRemoteError(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 800 {
		value = value[:800]
	}
	return value
}

func CollectMetrics(req Request, cfg Config) (Metrics, error) {
	metrics := Metrics{CollectedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	metrics.Hostname, _ = os.Hostname()
	readHostMetrics(&metrics, cfg.DataRoot)
	optionPath, err := optionFileForMetrics(req, cfg)
	if err != nil {
		return metrics, err
	}
	defer os.Remove(optionPath)
	mysql, err := mysqlBinary(cfg, req.Version, "mysql")
	if err != nil {
		return metrics, err
	}
	socket := filepath.Join(cfg.DataRoot, strconv.Itoa(req.Port), "mysql.sock")
	command := exec.Command(mysql, "--defaults-extra-file="+optionPath, "--batch", "--raw", "--skip-column-names", "-e", metricsQuery)
	output, commandErr := command.Output()
	if commandErr != nil {
		metrics.MySQLError = sanitizeRemoteError(commandErr.Error())
		return metrics, nil
	}
	values := map[string]int64{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(fields) != 2 {
			continue
		}
		if value, parseErr := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); parseErr == nil {
			values[fields[0]] = value
		}
	}
	metrics.MySQLUp = true
	metrics.MySQLUptime = values["Uptime"]
	metrics.ThreadsConnected = values["Threads_connected"]
	metrics.ThreadsRunning = values["Threads_running"]
	metrics.MaxUsedConnections = values["Max_used_connections"]
	metrics.MaxConnections = values["Max_connections"]
	metrics.Connections = values["Connections"]
	metrics.AbortedConnects = values["Aborted_connects"]
	metrics.ThreadsCreated = values["Threads_created"]
	metrics.Queries = values["Queries"]
	metrics.Questions = values["Questions"]
	metrics.SlowQueries = values["Slow_queries"]
	metrics.BytesReceived = values["Bytes_received"]
	metrics.BytesSent = values["Bytes_sent"]
	metrics.OpenTables = values["Open_tables"]
	metrics.TableLocksWaited = values["Table_locks_waited"]
	metrics.InnodbBufferPoolDataPages = values["Innodb_buffer_pool_pages_data"]
	metrics.InnodbBufferPoolFreePages = values["Innodb_buffer_pool_pages_free"]
	collectReplicationStatus(&metrics, mysql, optionPath)
	_ = socket // socket is encoded in the option file and kept explicit above.
	return metrics, nil
}

func optionFileForMetrics(req Request, cfg Config) (string, error) {
	dir, err := stagingDir(req, cfg)
	if err != nil {
		return "", err
	}
	socket := filepath.Join(cfg.DataRoot, strconv.Itoa(req.Port), "mysql.sock")
	return optionFile(dir, socket, req.Secrets.RootPassword)
}

func collectReplicationStatus(metrics *Metrics, mysql, optionPath string) {
	for _, query := range []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"} {
		output, err := exec.Command(mysql, "--defaults-extra-file="+optionPath, "--batch", "--raw", "-e", query).Output()
		if err != nil || strings.TrimSpace(string(output)) == "" {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) < 2 {
			return
		}
		headers := strings.Split(lines[0], "\t")
		values := strings.Split(lines[1], "\t")
		fields := map[string]string{}
		for index := range headers {
			if index < len(values) {
				fields[headers[index]] = values[index]
			}
		}
		metrics.ReplicationIO = firstNonEmpty(fields["Replica_IO_Running"], fields["Slave_IO_Running"])
		metrics.ReplicationSQL = firstNonEmpty(fields["Replica_SQL_Running"], fields["Slave_SQL_Running"])
		metrics.ReplicationLag, _ = strconv.ParseInt(firstNonEmpty(fields["Seconds_Behind_Source"], fields["Seconds_Behind_Master"]), 10, 64)
		return
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func readHostMetrics(metrics *Metrics, dataRoot string) {
	firstIdle, firstTotal := readCPUStat()
	time.Sleep(180 * time.Millisecond)
	secondIdle, secondTotal := readCPUStat()
	if secondTotal > firstTotal && secondIdle >= firstIdle {
		metrics.CPUPercent = math.Round((1-float64(secondIdle-firstIdle)/float64(secondTotal-firstTotal))*1000) / 10
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 3 {
			metrics.Load1, _ = strconv.ParseFloat(fields[0], 64)
			metrics.Load5, _ = strconv.ParseFloat(fields[1], 64)
			metrics.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	mem := map[string]uint64{}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if value, parseErr := strconv.ParseUint(fields[1], 10, 64); parseErr == nil {
					mem[strings.TrimSuffix(fields[0], ":")] = value / 1024
				}
			}
		}
	}
	metrics.MemoryTotalMB = mem["MemTotal"]
	metrics.MemoryAvailableMB = mem["MemAvailable"]
	if metrics.MemoryTotalMB >= metrics.MemoryAvailableMB {
		metrics.MemoryUsedMB = metrics.MemoryTotalMB - metrics.MemoryAvailableMB
	}
	metrics.SwapTotalMB = mem["SwapTotal"]
	if metrics.SwapTotalMB >= mem["SwapFree"] {
		metrics.SwapUsedMB = metrics.SwapTotalMB - mem["SwapFree"]
	}
	probePath := dataRoot
	var stat syscall.Statfs_t
	for {
		if err := syscall.Statfs(probePath, &stat); err == nil {
			metrics.DiskFreeMB = uint64(stat.Bavail) * uint64(stat.Bsize) / 1024 / 1024
			break
		}
		parent := filepath.Dir(probePath)
		if parent == probePath {
			break
		}
		probePath = parent
	}
}

func readCPUStat() (idle, total uint64) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		for index, field := range fields {
			value, parseErr := strconv.ParseUint(field, 10, 64)
			if parseErr != nil {
				continue
			}
			total += value
			if index == 3 || index == 4 {
				idle += value
			}
		}
		break
	}
	return idle, total
}

type cappedBuffer struct{ data []byte }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	const limit = 16 * 1024
	if len(b.data) < limit {
		remaining := limit - len(b.data)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return original, nil
}

func (b *cappedBuffer) String() string { return string(b.data) }
