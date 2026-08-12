package console

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aimdotsh/aim/internal/executor"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type BackupPlanInput struct {
	Name           string   `json:"name"`
	InstanceID     int64    `json:"instance_id"`
	Schedule       string   `json:"schedule"`
	AllDatabases   bool     `json:"all_databases"`
	Databases      []string `json:"databases"`
	RetentionDays  int      `json:"retention_days"`
	RetentionCount int      `json:"retention_count"`
	Enabled        bool     `json:"enabled"`
}

type BackupManager struct {
	Store      *Store
	Secrets    *SecretBox
	SSH        *SSHManager
	BackupRoot string
	mu         sync.Mutex
	running    map[string]context.CancelFunc
	scheduler  *cron.Cron
}

func NewBackupManager(store *Store, secrets *SecretBox, ssh *SSHManager, backupRoot string) *BackupManager {
	return &BackupManager{Store: store, Secrets: secrets, SSH: ssh, BackupRoot: backupRoot, running: map[string]context.CancelFunc{}}
}

func (m *BackupManager) StartScheduler() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadSchedulerLocked()
}

func (m *BackupManager) StopScheduler() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scheduler != nil {
		ctx := m.scheduler.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
		m.scheduler = nil
	}
}

func (m *BackupManager) ReloadScheduler() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloadSchedulerLocked()
}

func (m *BackupManager) reloadSchedulerLocked() error {
	if m.scheduler != nil {
		ctx := m.scheduler.Stop()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	scheduler := cron.New(cron.WithParser(parser))
	rows, err := m.Store.DB.Query(`SELECT id,schedule FROM backup_plans WHERE enabled=1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var schedule string
		if err := rows.Scan(&id, &schedule); err != nil {
			return err
		}
		planID := id
		if _, err := scheduler.AddFunc(schedule, func() {
			_, _ = m.StartPlan(context.Background(), nil, planID)
		}); err != nil {
			return fmt.Errorf("计划 %d 的 Cron 表达式无效: %w", id, err)
		}
	}
	scheduler.Start()
	m.scheduler = scheduler
	return nil
}

func (m *BackupManager) SavePlan(ctx context.Context, actor *User, input BackupPlanInput) (BackupPlan, error) {
	if actor == nil || !canOperate(actor.Role) {
		return BackupPlan{}, errors.New("没有创建备份计划的权限")
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		return BackupPlan{}, errors.New("备份计划名称不能为空且不能超过 80 个字符")
	}
	if input.InstanceID < 1 {
		return BackupPlan{}, errors.New("请选择 MySQL 实例")
	}
	if err := validateBackupSchedule(input.Schedule); err != nil {
		return BackupPlan{}, err
	}
	if input.RetentionDays < 0 || input.RetentionCount < 0 || input.RetentionDays > 3650 || input.RetentionCount > 10000 {
		return BackupPlan{}, errors.New("保留策略范围无效")
	}
	input.Databases = cleanBackupDatabases(input.Databases)
	if !input.AllDatabases && len(input.Databases) == 0 {
		return BackupPlan{}, errors.New("请选择全部数据库或至少一个数据库")
	}
	if !input.AllDatabases {
		for _, name := range input.Databases {
			if !validBackupDatabase(name) {
				return BackupPlan{}, fmt.Errorf("数据库名称无效: %s", name)
			}
		}
	}
	if input.RetentionDays == 0 && input.RetentionCount == 0 {
		input.RetentionCount = 30
	}
	if _, err := m.loadInstance(ctx, input.InstanceID); err != nil {
		return BackupPlan{}, errors.New("MySQL 实例不存在")
	}
	databasesJSON, _ := json.Marshal(input.Databases)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := m.Store.DB.ExecContext(ctx, `INSERT INTO backup_plans(name,instance_id,owner_id,schedule,all_databases,databases_json,retention_days,retention_count,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		input.Name, input.InstanceID, actor.ID, input.Schedule, boolInt(input.AllDatabases), string(databasesJSON), input.RetentionDays, input.RetentionCount, boolInt(input.Enabled), now, now)
	if err != nil {
		return BackupPlan{}, fmt.Errorf("保存备份计划失败: %w", err)
	}
	id, _ := result.LastInsertId()
	if err := m.ReloadScheduler(); err != nil {
		return BackupPlan{}, err
	}
	m.Store.Audit(ctx, actor, "", "backup_plan_create", "backup_plan", fmt.Sprint(id), fmt.Sprintf(`{"schedule":%q}`, input.Schedule))
	return m.LoadPlan(ctx, id)
}

func (m *BackupManager) DeletePlan(ctx context.Context, actor *User, id int64) error {
	plan, err := m.LoadPlan(ctx, id)
	if err != nil {
		return err
	}
	if actor == nil || (actor.Role != "admin" && plan.OwnerID != actor.ID) || !canOperate(actor.Role) {
		return errors.New("没有删除该备份计划的权限")
	}
	if _, err := m.Store.DB.ExecContext(ctx, `DELETE FROM backup_plans WHERE id=?`, id); err != nil {
		return err
	}
	_ = m.ReloadScheduler()
	m.Store.Audit(ctx, actor, "", "backup_plan_delete", "backup_plan", fmt.Sprint(id), plan.Name)
	return nil
}

func (m *BackupManager) Plans(ctx context.Context, actor *User) ([]BackupPlan, error) {
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT p.id,p.name,p.instance_id,i.version,i.port,h.name,h.address,p.schedule,p.all_databases,p.databases_json,p.retention_days,p.retention_count,p.enabled,p.owner_id,u.username,p.created_at,p.updated_at
        FROM backup_plans p JOIN instances i ON i.id=p.instance_id JOIN hosts h ON h.id=i.host_id JOIN users u ON u.id=p.owner_id ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BackupPlan{}
	for rows.Next() {
		var plan BackupPlan
		var all, enabled int
		if err := rows.Scan(&plan.ID, &plan.Name, &plan.InstanceID, &plan.Version, &plan.Port, &plan.HostName, &plan.Address, &plan.Schedule, &all, &plan.DatabasesJSON, &plan.RetentionDays, &plan.RetentionCount, &enabled, &plan.OwnerID, &plan.OwnerUsername, &plan.CreatedAt, &plan.UpdatedAt); err != nil {
			continue
		}
		if actor != nil && actor.Role != "admin" && plan.OwnerID != actor.ID {
			continue
		}
		plan.AllDatabases, plan.Enabled = all == 1, enabled == 1
		_ = json.Unmarshal([]byte(plan.DatabasesJSON), &plan.Databases)
		items = append(items, plan)
	}
	return items, rows.Err()
}

func (m *BackupManager) LoadPlan(ctx context.Context, id int64) (BackupPlan, error) {
	plans, err := m.Plans(ctx, nil)
	if err != nil {
		return BackupPlan{}, err
	}
	for _, plan := range plans {
		if plan.ID == id {
			return plan, nil
		}
	}
	return BackupPlan{}, errors.New("备份计划不存在")
}

func (m *BackupManager) StartPlan(ctx context.Context, actor *User, planID int64) (BackupRun, error) {
	plan, err := m.LoadPlan(ctx, planID)
	if err != nil {
		return BackupRun{}, err
	}
	if actor == nil {
		actor = &User{ID: plan.OwnerID, Username: plan.OwnerUsername, Role: "operator", Active: true}
	}
	if actor.Role != "admin" && plan.OwnerID != actor.ID {
		return BackupRun{}, errors.New("没有执行该备份计划的权限")
	}
	if !canOperate(actor.Role) {
		return BackupRun{}, errors.New("没有执行备份的权限")
	}
	var running int
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE plan_id=? AND status IN ('queued','running')`, planID).Scan(&running); err != nil {
		return BackupRun{}, err
	}
	if running > 0 {
		return BackupRun{}, errors.New("该备份计划已有任务正在运行")
	}
	runID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := m.Store.DB.ExecContext(ctx, `INSERT INTO backup_runs(id,plan_id,instance_id,owner_id,status,started_at) VALUES(?,?,?,?,?,?)`, runID, plan.ID, plan.InstanceID, plan.OwnerID, "queued", now); err != nil {
		return BackupRun{}, err
	}
	m.mu.Lock()
	jobCtx, cancel := context.WithCancel(context.Background())
	m.running[runID] = cancel
	m.mu.Unlock()
	m.Store.Audit(ctx, actor, "", "backup_start", "backup_run", runID, plan.Name)
	go m.execute(jobCtx, runID, plan, *actor)
	return m.LoadRun(ctx, actor, runID)
}

func (m *BackupManager) CancelRun(ctx context.Context, actor *User, id string) error {
	run, err := m.LoadRun(ctx, actor, id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	cancel := m.running[id]
	m.mu.Unlock()
	if cancel == nil || (actor != nil && actor.Role != "admin" && run.OwnerID != actor.ID) {
		return errors.New("任务当前不可取消")
	}
	cancel()
	return nil
}

func (m *BackupManager) execute(ctx context.Context, runID string, plan BackupPlan, actor User) {
	defer func() {
		m.mu.Lock()
		delete(m.running, runID)
		m.mu.Unlock()
	}()
	started := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.Store.DB.Exec(`UPDATE backup_runs SET status='running',started_at=? WHERE id=?`, started, runID)
	instance, err := m.loadInstance(context.Background(), plan.InstanceID)
	if err != nil {
		m.finishRun(runID, "failed", "实例不存在", "", 0, "")
		return
	}
	host, key, err := m.SSH.LoadHost(context.Background(), instance.HostID)
	if err != nil {
		m.finishRun(runID, "failed", "读取主机凭据失败: "+err.Error(), "", 0, "")
		return
	}
	rootPassword, err := m.decryptSecret(instance.RootSecretID)
	if err != nil {
		m.finishRun(runID, "failed", "读取 MySQL 凭据失败", "", 0, "")
		return
	}
	req := executor.Request{Protocol: executor.ProtocolVersion, RequestID: runID, Action: "backup", Version: instance.Version, Port: instance.Port,
		Secrets: executor.Secrets{RootPassword: rootPassword}, Backup: &executor.BackupSpec{AllDatabases: plan.AllDatabases, Databases: plan.Databases}}
	var result executor.BackupResult
	err = m.SSH.RunExecutor(ctx, host, key, req, func(event executor.Event) {
		if event.Backup != nil {
			result = *event.Backup
		}
	})
	if err != nil {
		m.finishRun(runID, "failed", err.Error(), "", 0, "")
		return
	}
	ownerDir := backupPathSegment(actor.Username)
	planDir := backupPathSegment(plan.Name)
	now := time.Now().UTC()
	destinationDir := filepath.Join(m.BackupRoot, ownerDir, planDir, now.Format("2006"), now.Format("01"))
	fileName := fmt.Sprintf("%s_%s_%s.sql.gz", planDir, now.Format("20060102_150405"), runID[:8])
	localPath := filepath.Join(destinationDir, fileName)
	if err := m.SSH.DownloadStagedFile(ctx, host, key, runID, result.RemotePath, localPath, result.Size, nil); err != nil {
		_ = m.SSH.RemoveStagedFile(context.Background(), host, key, runID, result.RemotePath)
		m.finishRun(runID, "failed", "备份下载失败: "+err.Error(), "", 0, "")
		return
	}
	_ = m.SSH.RemoveStagedFile(context.Background(), host, key, runID, result.RemotePath)
	actualHash, hashErr := fileSHA256(localPath)
	if hashErr != nil || !strings.EqualFold(actualHash, result.SHA256) {
		_ = os.Remove(localPath)
		m.finishRun(runID, "failed", "备份下载后 SHA-256 校验失败", "", 0, "")
		return
	}
	manifest := map[string]any{"format": 1, "run_id": runID, "plan": plan.Name, "instance": instance.Name, "host": host.Address, "port": instance.Port, "all_databases": plan.AllDatabases, "databases": plan.Databases, "started_at": result.StartedAt, "finished_at": result.FinishedAt, "size": result.Size, "sha256": result.SHA256, "compression": "gzip", "dump_client": result.DumpClient, "consistency": "single-transaction (InnoDB)"}
	if raw, marshalErr := json.MarshalIndent(manifest, "", "  "); marshalErr == nil {
		_ = os.WriteFile(localPath+".manifest.json", raw, 0o600)
	}
	m.finishRun(runID, "success", "在线一致性备份完成并通过 SHA-256 校验", localPath, result.Size, result.SHA256)
	m.applyRetention(plan)
}

func (m *BackupManager) finishRun(id, status, message, path string, size int64, checksum string) {
	finished := time.Now().UTC().Format(time.RFC3339Nano)
	name := ""
	if path != "" {
		name = filepath.Base(path)
	}
	_, _ = m.Store.DB.Exec(`UPDATE backup_runs SET status=?,file_name=?,file_path=?,size=?,sha256=?,message=?,finished_at=? WHERE id=?`, status, name, path, size, checksum, message, finished, id)
}

func (m *BackupManager) PlansForUser(ctx context.Context, actor *User) ([]BackupPlan, error) {
	return m.Plans(ctx, actor)
}

func (m *BackupManager) Runs(ctx context.Context, actor *User) ([]BackupRun, error) {
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT r.id,r.plan_id,r.instance_id,r.owner_id,r.status,r.file_name,r.file_path,r.size,r.sha256,r.message,r.started_at,r.finished_at,p.name,u.username FROM backup_runs r JOIN backup_plans p ON p.id=r.plan_id JOIN users u ON u.id=r.owner_id ORDER BY r.started_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BackupRun{}
	for rows.Next() {
		var run BackupRun
		if err := rows.Scan(&run.ID, &run.PlanID, &run.InstanceID, &run.OwnerID, &run.Status, &run.FileName, &run.FilePath, &run.Size, &run.SHA256, &run.Message, &run.StartedAt, &run.FinishedAt, &run.PlanName, &run.OwnerUsername); err != nil {
			continue
		}
		if actor != nil && actor.Role != "admin" && run.OwnerID != actor.ID {
			continue
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

func (m *BackupManager) LoadRun(ctx context.Context, actor *User, id string) (BackupRun, error) {
	runs, err := m.Runs(ctx, actor)
	if err != nil {
		return BackupRun{}, err
	}
	for _, run := range runs {
		if run.ID == id {
			return run, nil
		}
	}
	return BackupRun{}, errors.New("备份任务不存在")
}

func (m *BackupManager) Metrics(ctx context.Context, actor *User, instanceID int64) (executor.Metrics, error) {
	if actor == nil {
		return executor.Metrics{}, errors.New("未登录")
	}
	instance, err := m.loadInstance(ctx, instanceID)
	if err != nil {
		return executor.Metrics{}, err
	}
	host, key, err := m.SSH.LoadHost(ctx, instance.HostID)
	if err != nil {
		return executor.Metrics{}, err
	}
	rootPassword, err := m.decryptSecret(instance.RootSecretID)
	if err != nil {
		return executor.Metrics{}, errors.New("读取 MySQL 凭据失败")
	}
	req := executor.Request{Protocol: executor.ProtocolVersion, RequestID: "metrics-" + uuid.NewString(), Action: "metrics", Version: instance.Version, Port: instance.Port, Secrets: executor.Secrets{RootPassword: rootPassword}}
	var metrics executor.Metrics
	if err := m.SSH.RunExecutor(ctx, host, key, req, func(event executor.Event) {
		if event.Metrics != nil {
			metrics = *event.Metrics
		}
	}); err != nil {
		return executor.Metrics{}, err
	}
	raw, _ := json.Marshal(metrics)
	_, _ = m.Store.DB.ExecContext(ctx, `INSERT INTO monitor_samples(instance_id,collected_at,metrics_json) VALUES(?,?,?)`, instanceID, metrics.CollectedAt, string(raw))
	_, _ = m.Store.DB.ExecContext(ctx, `DELETE FROM monitor_samples WHERE instance_id=? AND id NOT IN (SELECT id FROM monitor_samples WHERE instance_id=? ORDER BY collected_at DESC LIMIT 240)`, instanceID, instanceID)
	m.Store.Audit(ctx, actor, "", "monitor_collect", "instance", fmt.Sprint(instanceID), fmt.Sprintf(`{"mysql_up":%t}`, metrics.MySQLUp))
	return metrics, nil
}

func (m *BackupManager) MetricHistory(ctx context.Context, actor *User, instanceID int64) ([]MonitorSample, error) {
	if _, err := m.loadInstance(ctx, instanceID); err != nil {
		return nil, err
	}
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT id,instance_id,collected_at,metrics_json FROM monitor_samples WHERE instance_id=? ORDER BY collected_at DESC LIMIT 120`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []MonitorSample{}
	for rows.Next() {
		var sample MonitorSample
		if rows.Scan(&sample.ID, &sample.InstanceID, &sample.CollectedAt, &sample.MetricsJSON) == nil {
			items = append(items, sample)
		}
	}
	return items, rows.Err()
}

type backupInstance struct {
	ID, HostID, RootSecretID int64
	Name, Version            string
	Port                     int
}

func (m *BackupManager) loadInstance(ctx context.Context, id int64) (backupInstance, error) {
	var item backupInstance
	var rootSecret sql.NullInt64
	var specJSON string
	err := m.Store.DB.QueryRowContext(ctx, `SELECT i.id,i.host_id,i.version,i.port,i.root_secret_id,i.spec_json FROM instances i WHERE i.id=?`, id).
		Scan(&item.ID, &item.HostID, &item.Version, &item.Port, &rootSecret, &specJSON)
	if rootSecret.Valid {
		item.RootSecretID = rootSecret.Int64
	}
	if err == nil && specJSON != "" {
		var spec struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(specJSON), &spec) == nil {
			item.Name = strings.TrimSpace(spec.Name)
		}
	}
	return item, err
}

func (m *BackupManager) decryptSecret(id int64) (string, error) {
	var cipherText string
	if err := m.Store.DB.QueryRow(`SELECT cipher_text FROM secrets WHERE id=?`, id).Scan(&cipherText); err != nil {
		return "", err
	}
	plain, err := m.Secrets.Decrypt(cipherText)
	return string(plain), err
}

func validateBackupSchedule(value string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if strings.TrimSpace(value) == "" {
		return errors.New("Cron 表达式不能为空")
	}
	if _, err := parser.Parse(value); err != nil {
		return fmt.Errorf("Cron 表达式无效: %w", err)
	}
	return nil
}

func cleanBackupDatabases(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, raw := range values {
		for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
			value = strings.TrimSpace(value)
			if value != "" && !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func validBackupDatabase(value string) bool {
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

func backupPathSegment(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "..", "_")
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) || (r >= 0x4e00 && r <= 0x9fff) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	result := strings.Trim(b.String(), "._-")
	if result == "" {
		return "unknown"
	}
	if len([]rune(result)) > 80 {
		return string([]rune(result)[:80])
	}
	return result
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *BackupManager) applyRetention(plan BackupPlan) {
	runs, err := m.Runs(context.Background(), &User{ID: plan.OwnerID, Username: plan.OwnerUsername, Role: "operator", Active: true})
	if err != nil {
		return
	}
	filtered := make([]BackupRun, 0)
	for _, run := range runs {
		if run.PlanID == plan.ID && run.Status == "success" && run.FilePath != "" {
			filtered = append(filtered, run)
		}
	}
	slices.SortFunc(filtered, func(a, b BackupRun) int { return strings.Compare(b.StartedAt, a.StartedAt) })
	cutoff := time.Now().UTC().AddDate(0, 0, -plan.RetentionDays)
	for index, run := range filtered {
		started, _ := time.Parse(time.RFC3339Nano, run.StartedAt)
		deleteByCount := plan.RetentionCount > 0 && index >= plan.RetentionCount
		deleteByAge := plan.RetentionDays > 0 && !started.IsZero() && started.Before(cutoff)
		if !deleteByCount && !deleteByAge || !m.safeBackupPath(run.FilePath) {
			continue
		}
		_ = os.Remove(run.FilePath)
		_ = os.Remove(run.FilePath + ".manifest.json")
		_, _ = m.Store.DB.Exec(`DELETE FROM backup_runs WHERE id=?`, run.ID)
	}
}

func (m *BackupManager) safeBackupPath(path string) bool {
	root, err := filepath.Abs(m.BackupRoot)
	if err != nil {
		return false
	}
	actual, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return actual != root && strings.HasPrefix(actual, root+string(filepath.Separator))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func canOperate(role string) bool { return role == "admin" || role == "operator" }

func serveBackupFile(w http.ResponseWriter, r *http.Request, run BackupRun) {
	file, err := os.Open(run.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "备份文件不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "备份文件不可用")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(run.FileName)))
	http.ServeContent(w, r, run.FileName, info.ModTime(), file)
}
