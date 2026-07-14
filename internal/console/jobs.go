package console

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aimdotsh/aim/internal/executor"
	"github.com/google/uuid"
)

var mysqlVersionPattern = regexp.MustCompile(`^(5\.6|5\.7|8\.0|8\.4)\.[0-9]+$`)

var jobStateTransitions = map[string]map[string]bool{
	"queued":             {"preflight": true, "running": true, "failed": true, "needs_verification": true},
	"preflight":          {"transferring": true, "running": true, "failed": true, "needs_verification": true},
	"transferring":       {"running": true, "failed": true, "needs_verification": true},
	"running":            {"complete": true, "failed": true, "needs_verification": true},
	"needs_verification": {"failed": true},
}

type DeploymentNode struct {
	HostID     int64  `json:"host_id"`
	LocalIP    string `json:"local_ip,omitempty"`
	ServerID   uint32 `json:"server_id,omitempty"`
	RootSecret int64  `json:"root_secret_id,omitempty"`
}

type DeploymentRequest struct {
	Name                string           `json:"name"`
	Mode                string           `json:"mode"`
	Version             string           `json:"version"`
	Port                int              `json:"port"`
	BindAddress         string           `json:"bind_address"`
	MediaID             int64            `json:"media_id,omitempty"`
	Nodes               []DeploymentNode `json:"nodes"`
	ReplicaHost         string           `json:"replica_host,omitempty"`
	SourceHost          string           `json:"source_host,omitempty"`
	SourcePort          int              `json:"source_port,omitempty"`
	ReplicationUser     string           `json:"replication_user,omitempty"`
	MGRPort             int              `json:"mgr_port,omitempty"`
	MGRGroupName        string           `json:"mgr_group_name,omitempty"`
	MGRAllowlist        string           `json:"mgr_allowlist,omitempty"`
	MGRRecoveryUser     string           `json:"mgr_recovery_user,omitempty"`
	RootPassword        string           `json:"root_password,omitempty"`
	ReplicationPassword string           `json:"replication_password,omitempty"`
	SourcePassword      string           `json:"source_password,omitempty"`
	MGRRecoveryPassword string           `json:"mgr_recovery_password,omitempty"`
	ReplicationSecretID int64            `json:"replication_secret_id,omitempty"`
	SourceSecretID      int64            `json:"source_secret_id,omitempty"`
	MGRRecoverySecretID int64            `json:"mgr_recovery_secret_id,omitempty"`
}

type JobManager struct {
	Store   *Store
	Secrets *SecretBox
	SSH     *SSHManager
}

type InstanceActionInput struct {
	Action       string `json:"action"`
	DryRun       bool   `json:"dry_run,omitempty"`
	PreviewJobID string `json:"preview_job_id,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
}

type instanceActionPayload struct {
	InstanceID int64  `json:"instance_id"`
	Action     string `json:"action"`
	DryRun     bool   `json:"dry_run"`
	Confirm    bool   `json:"confirm"`
}

type deploymentTarget struct {
	node       DeploymentNode
	host       Host
	privateKey []byte
	root       string
	facts      executor.HostFacts
}

func (m *JobManager) CreateDeployment(ctx context.Context, user *User, remoteAddr string, input DeploymentRequest) (string, error) {
	if err := validateDeployment(&input); err != nil {
		return "", err
	}
	if input.MGRGroupName == "" && input.Mode == "mgr" {
		input.MGRGroupName = uuid.NewString()
	}
	if input.RootPassword == "" {
		input.RootPassword, _ = randomToken(24)
	}
	for index := range input.Nodes {
		secretID, err := m.saveSecret(ctx, user.ID, fmt.Sprintf("%s-%d-root", input.Name, input.Nodes[index].HostID), "mysql_root", input.RootPassword)
		if err != nil {
			return "", err
		}
		input.Nodes[index].RootSecret = secretID
	}
	if input.Mode == "source" || input.Mode == "replication" {
		if input.ReplicationPassword == "" {
			input.ReplicationPassword, _ = randomToken(24)
		}
		id, err := m.saveSecret(ctx, user.ID, input.Name+"-replication", "mysql_replication", input.ReplicationPassword)
		if err != nil {
			return "", err
		}
		input.ReplicationSecretID = id
	}
	if input.Mode == "replica" && input.SourcePassword != "" {
		id, err := m.saveSecret(ctx, user.ID, input.Name+"-source", "mysql_replication", input.SourcePassword)
		if err != nil {
			return "", err
		}
		input.SourceSecretID = id
	}
	if input.Mode == "mgr" {
		if input.MGRRecoveryPassword == "" {
			input.MGRRecoveryPassword, _ = randomToken(24)
		}
		id, err := m.saveSecret(ctx, user.ID, input.Name+"-mgr-recovery", "mysql_mgr_recovery", input.MGRRecoveryPassword)
		if err != nil {
			return "", err
		}
		input.MGRRecoverySecretID = id
	}
	// Never persist plaintext request secrets in job payloads.
	input.RootPassword, input.ReplicationPassword, input.SourcePassword, input.MGRRecoveryPassword = "", "", "", ""
	payload, _ := json.Marshal(input)
	jobID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at) VALUES(?,'deployment','queued',?,?,?)`, jobID, string(payload), user.ID, now); err != nil {
		return "", err
	}
	for index, node := range input.Nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_hosts(job_id,host_id,step_order) VALUES(?,?,?)`, jobID, node.HostID, index); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO host_locks(host_id,job_id,acquired_at) VALUES(?,?,?)`, node.HostID, jobID, now); err != nil {
			return "", fmt.Errorf("主机 %d 已有任务正在执行", node.HostID)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	m.Store.Audit(ctx, user, remoteAddr, "deployment_create", "job", jobID, fmt.Sprintf(`{"mode":%q,"version":%q,"port":%d}`, input.Mode, input.Version, input.Port))
	go m.runDeployment(jobID, input)
	return jobID, nil
}

func (m *JobManager) RetryDeployment(ctx context.Context, user *User, remoteAddr, previousJobID string) (string, error) {
	var kind, state, payloadJSON string
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT kind,state,payload_json FROM jobs WHERE id=?`, previousJobID).Scan(&kind, &state, &payloadJSON); err != nil {
		return "", errors.New("找不到原部署任务")
	}
	if kind != "deployment" || state != "failed" {
		return "", errors.New("只有失败的部署任务可以从未完成节点重试")
	}
	var deployment DeploymentRequest
	if err := json.Unmarshal([]byte(payloadJSON), &deployment); err != nil {
		return "", errors.New("原部署规格已损坏")
	}
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT host_id,state,step_order FROM job_hosts WHERE job_id=? ORDER BY step_order`, previousJobID)
	if err != nil {
		return "", err
	}
	type hostStep struct {
		hostID int64
		state  string
		order  int
	}
	steps := []hostStep{}
	for rows.Next() {
		var step hostStep
		if err := rows.Scan(&step.hostID, &step.state, &step.order); err != nil {
			rows.Close()
			return "", err
		}
		steps = append(steps, step)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	jobID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at) VALUES(?,'deployment','queued',?,?,?)`, jobID, payloadJSON, user.ID, now); err != nil {
		return "", err
	}
	incomplete := 0
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_hosts(job_id,host_id,step_order,state) VALUES(?,?,?,?)`, jobID, step.hostID, step.order, step.state); err != nil {
			return "", err
		}
		if step.state == "complete" {
			continue
		}
		incomplete++
		if _, err := tx.ExecContext(ctx, `INSERT INTO host_locks(host_id,job_id,acquired_at) VALUES(?,?,?)`, step.hostID, jobID, now); err != nil {
			return "", fmt.Errorf("主机 %d 已有任务正在执行", step.hostID)
		}
	}
	if incomplete == 0 {
		return "", errors.New("原任务没有可重试的未完成节点")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	m.Store.Audit(ctx, user, remoteAddr, "deployment_retry", "job", jobID, fmt.Sprintf(`{"previous_job_id":%q}`, previousJobID))
	go m.runDeployment(jobID, deployment)
	return jobID, nil
}

func (m *JobManager) VerifyInterruptedDeployment(ctx context.Context, user *User, remoteAddr, jobID string) error {
	var kind, state, payloadJSON string
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT kind,state,payload_json FROM jobs WHERE id=?`, jobID).Scan(&kind, &state, &payloadJSON); err != nil {
		return errors.New("找不到待核实任务")
	}
	if kind != "deployment" || state != "needs_verification" {
		return errors.New("任务不处于待核实状态")
	}
	var deployment DeploymentRequest
	if err := json.Unmarshal([]byte(payloadJSON), &deployment); err != nil {
		return errors.New("部署规格已损坏")
	}
	completed := map[int64]bool{}
	rows, err := m.Store.DB.QueryContext(ctx, `SELECT host_id,state FROM job_hosts WHERE job_id=?`, jobID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var hostID int64
		var hostState string
		if rows.Scan(&hostID, &hostState) == nil {
			completed[hostID] = hostState == "complete"
		}
	}
	_ = rows.Close()
	for _, node := range deployment.Nodes {
		host, _, err := m.SSH.LoadHost(ctx, node.HostID)
		if err != nil {
			return err
		}
		ports := []int{deployment.Port}
		if deployment.Mode == "mgr" {
			ports = append(ports, deployment.MGRPort)
		}
		facts, err := m.SSH.Probe(ctx, node.HostID, ports)
		if err != nil {
			return fmt.Errorf("核实主机 %s 失败: %w", host.Name, err)
		}
		if completed[node.HostID] {
			if facts.Ports[deployment.Port] != "listening" || (deployment.Mode == "mgr" && facts.Ports[deployment.MGRPort] != "listening") {
				return fmt.Errorf("已完成节点 %s 不再在线，请先人工检查", host.Name)
			}
			continue
		}
		if facts.Ports[deployment.Port] == "listening" {
			return fmt.Errorf("未完成节点 %s 的端口 %d 正在监听，无法安全判断远端执行结果", host.Name, deployment.Port)
		}
	}
	if err := m.transitionJob(jobID, "failed"); err != nil {
		return err
	}
	_, _ = m.Store.DB.ExecContext(ctx, `UPDATE jobs SET error='中断状态已核实，可从未完成节点重试',completed_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), jobID)
	m.Store.Audit(ctx, user, remoteAddr, "deployment_verify_interrupted", "job", jobID, `{}`)
	return nil
}

func (m *JobManager) CreateInstanceAction(ctx context.Context, user *User, remoteAddr string, instanceID int64, input InstanceActionInput) (string, error) {
	if input.Action != "start" && input.Action != "stop" && input.Action != "status" && input.Action != "reinitialize" && input.Action != "uninstall" {
		return "", errors.New("不支持的实例操作")
	}
	var hostID int64
	var address string
	var port int
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT i.host_id,h.address,i.port FROM instances i JOIN hosts h ON h.id=i.host_id WHERE i.id=?`, instanceID).Scan(&hostID, &address, &port); err != nil {
		return "", err
	}
	destructive := input.Action == "reinitialize" || input.Action == "uninstall"
	confirm := false
	confirmationHash := ""
	var previewExpires any
	if destructive {
		expected := fmt.Sprintf("%s:%d", address, port)
		if input.DryRun {
			confirmationHash = hex.EncodeToString(tokenHash(expected))
			previewExpires = time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339Nano)
		} else {
			if input.PreviewJobID == "" || input.Confirmation == "" {
				return "", errors.New("破坏性操作必须先完成 dry-run 并输入主机地址:端口确认")
			}
			var state, hash, expires, payloadJSON string
			if err := m.Store.DB.QueryRowContext(ctx, `SELECT state,confirmation_hash,preview_expires_at,payload_json FROM jobs WHERE id=? AND kind='instance_action'`, input.PreviewJobID).
				Scan(&state, &hash, &expires, &payloadJSON); err != nil {
				return "", errors.New("找不到对应的预览任务")
			}
			var preview instanceActionPayload
			_ = json.Unmarshal([]byte(payloadJSON), &preview)
			expiresAt, _ := time.Parse(time.RFC3339Nano, expires)
			if state != "complete" || preview.InstanceID != instanceID || preview.Action != input.Action || !preview.DryRun || time.Now().UTC().After(expiresAt) {
				return "", errors.New("预览任务无效或已超过 15 分钟")
			}
			if hex.EncodeToString(tokenHash(input.Confirmation)) != hash {
				return "", errors.New("确认文本不匹配")
			}
			confirm = true
		}
	}
	payload := instanceActionPayload{InstanceID: instanceID, Action: input.Action, DryRun: input.DryRun, Confirm: confirm}
	payloadJSON, _ := json.Marshal(payload)
	jobID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,state,payload_json,created_by,created_at,confirmation_hash,preview_expires_at) VALUES(?,'instance_action','queued',?,?,?,?,?)`,
		jobID, string(payloadJSON), user.ID, now, confirmationHash, previewExpires); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_hosts(job_id,host_id,step_order) VALUES(?,?,0)`, jobID, hostID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO host_locks(host_id,job_id,acquired_at) VALUES(?,?,?)`, hostID, jobID, now); err != nil {
		return "", errors.New("该主机已有任务正在执行")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	m.Store.Audit(ctx, user, remoteAddr, "instance_"+input.Action, "instance", strconv.FormatInt(instanceID, 10), fmt.Sprintf(`{"dry_run":%t}`, input.DryRun))
	go m.runInstanceAction(jobID, payload)
	return jobID, nil
}

func (m *JobManager) runInstanceAction(jobID string, payload instanceActionPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	defer m.releaseLocks(jobID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := m.transitionJob(jobID, "running"); err != nil {
		m.fail(jobID, err)
		return
	}
	_, _ = m.Store.DB.Exec(`UPDATE jobs SET started_at=? WHERE id=?`, now, jobID)
	var hostID, rootSecretID int64
	var clusterID sql.NullInt64
	var version, role, specJSON string
	var port int
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT host_id,version,port,role,root_secret_id,cluster_id,spec_json FROM instances WHERE id=?`, payload.InstanceID).
		Scan(&hostID, &version, &port, &role, &rootSecretID, &clusterID, &specJSON); err != nil {
		m.fail(jobID, err)
		return
	}
	host, key, err := m.SSH.LoadHost(ctx, hostID)
	if err != nil {
		m.fail(jobID, err)
		return
	}
	rootPassword, err := m.readSecret(ctx, rootSecretID)
	if err != nil {
		m.fail(jobID, err)
		return
	}
	req := executor.Request{Protocol: executor.ProtocolVersion, RequestID: jobID, Action: payload.Action, Version: version, Port: port, Role: role, DryRun: payload.DryRun, Confirm: payload.Confirm,
		Secrets: executor.Secrets{RootPassword: rootPassword}}
	if payload.Action == "reinitialize" {
		var spec DeploymentRequest
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			m.fail(jobID, errors.New("实例部署规格已损坏，拒绝重新初始化"))
			return
		}
		req.BindAddress = spec.BindAddress
		var current DeploymentNode
		for _, node := range spec.Nodes {
			if node.HostID == hostID {
				current = node
				break
			}
		}
		req.ServerID = current.ServerID
		replSecret, _ := m.readSecret(ctx, spec.ReplicationSecretID)
		sourceSecret, _ := m.readSecret(ctx, spec.SourceSecretID)
		mgrSecret, _ := m.readSecret(ctx, spec.MGRRecoverySecretID)
		req.Secrets.ReplicationPassword = replSecret
		req.Secrets.SourcePassword = sourceSecret
		req.Secrets.MGRRecoveryPassword = mgrSecret
		if role == "source" {
			replicaHost := spec.ReplicaHost
			if spec.Mode == "replication" && len(spec.Nodes) == 2 {
				replicaHost = spec.Nodes[1].LocalIP
			}
			req.Replication = executor.Replication{ReplicaHost: replicaHost, User: spec.ReplicationUser}
		}
		if role == "replica" {
			sourceHost := spec.SourceHost
			if spec.Mode == "replication" && len(spec.Nodes) == 2 {
				sourceHost = spec.Nodes[0].LocalIP
				req.Secrets.SourcePassword = replSecret
			}
			req.Replication = executor.Replication{SourceHost: sourceHost, SourcePort: spec.SourcePort, User: spec.ReplicationUser}
		}
		if role == "mgr" {
			seeds := make([]string, 0, len(spec.Nodes))
			for _, node := range spec.Nodes {
				seeds = append(seeds, net.JoinHostPort(node.LocalIP, strconv.Itoa(spec.MGRPort)))
			}
			req.MGR = executor.MGR{LocalAddress: current.LocalIP, Port: spec.MGRPort, Seeds: seeds, GroupName: spec.MGRGroupName, Allowlist: spec.MGRAllowlist, RecoveryUser: spec.MGRRecoveryUser, Bootstrap: false}
		}
	}
	m.log(jobID, "info", "execute", fmt.Sprintf("在 %s 执行 %s", host.Name, payload.Action))
	remoteState := ""
	if err := m.SSH.RunExecutor(ctx, host, key, req, func(event executor.Event) {
		if event.Message != "" {
			var result struct {
				State string `json:"state"`
				OK    bool   `json:"ok"`
			}
			if json.Unmarshal([]byte(event.Message), &result) == nil && result.OK && result.State != "" {
				remoteState = result.State
			}
			m.log(jobID, event.Level, event.Phase, host.Name+": "+event.Message)
		}
	}); err != nil {
		m.fail(jobID, err)
		return
	}
	if !payload.DryRun {
		switch payload.Action {
		case "start", "reinitialize":
			_, _ = m.Store.DB.Exec(`UPDATE instances SET state='running',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), payload.InstanceID)
		case "stop":
			_, _ = m.Store.DB.Exec(`UPDATE instances SET state='stopped',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), payload.InstanceID)
		case "status":
			if remoteState != "" {
				_, _ = m.Store.DB.Exec(`UPDATE instances SET state=?,updated_at=? WHERE id=?`, remoteState, time.Now().UTC().Format(time.RFC3339Nano), payload.InstanceID)
			}
		case "uninstall":
			_, _ = m.Store.DB.Exec(`DELETE FROM instances WHERE id=?`, payload.InstanceID)
		}
		if clusterID.Valid {
			if payload.Action == "stop" || payload.Action == "uninstall" {
				_, _ = m.Store.DB.Exec(`UPDATE clusters SET state='degraded',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), clusterID.Int64)
			} else if payload.Action == "start" || payload.Action == "reinitialize" {
				var notRunning int
				_ = m.Store.DB.QueryRow(`SELECT COUNT(*) FROM instances WHERE cluster_id=? AND state!='running'`, clusterID.Int64).Scan(&notRunning)
				if notRunning == 0 {
					_, _ = m.Store.DB.Exec(`UPDATE clusters SET state='online',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), clusterID.Int64)
				}
			}
		}
	}
	m.complete(jobID, fmt.Sprintf(`{"action":%q,"dry_run":%t}`, payload.Action, payload.DryRun))
}

func validateDeployment(input *DeploymentRequest) error {
	if !mysqlVersionPattern.MatchString(input.Version) {
		return errors.New("MySQL 版本必须为受支持的精确版本")
	}
	if input.Port < 1 || input.Port > 65535 {
		return errors.New("MySQL 端口无效")
	}
	if input.Name == "" || len(input.Name) > 80 {
		return errors.New("部署名称不能为空且不能超过 80 个字符")
	}
	if input.BindAddress == "" {
		input.BindAddress = "0.0.0.0"
	}
	if input.BindAddress != "0.0.0.0" && net.ParseIP(input.BindAddress) == nil {
		return errors.New("绑定地址无效")
	}
	expectedNodes := map[string]int{"standalone": 1, "source": 1, "replica": 1, "replication": 2, "mgr": 3}
	count, ok := expectedNodes[input.Mode]
	if !ok || len(input.Nodes) != count {
		return errors.New("部署模式与节点数量不匹配")
	}
	seen := map[int64]bool{}
	for _, node := range input.Nodes {
		if node.HostID < 1 || seen[node.HostID] {
			return errors.New("部署节点无效或重复")
		}
		seen[node.HostID] = true
	}
	if input.ReplicationUser == "" {
		input.ReplicationUser = "aim_repl"
	}
	if input.SourcePort == 0 {
		input.SourcePort = input.Port
	}
	if input.Mode == "replica" && (net.ParseIP(input.SourceHost) == nil || input.SourcePassword == "") {
		return errors.New("从库部署需要源库地址和复制密码")
	}
	if input.Mode == "source" && input.ReplicaHost == "" {
		input.ReplicaHost = "%"
	}
	if input.Mode == "source" || input.Mode == "replica" || input.Mode == "replication" {
		serverIDs := map[uint32]bool{}
		for _, node := range input.Nodes {
			if node.ServerID == 0 || serverIDs[node.ServerID] {
				return errors.New("复制节点需要非零且唯一的 server_id")
			}
			serverIDs[node.ServerID] = true
		}
	}
	if input.Mode == "mgr" {
		if !strings.HasPrefix(input.Version, "8.0.") || compareVersion(input.Version, "8.0.23") < 0 || input.MGRPort < 1 || input.MGRPort > 65535 || input.MGRPort == input.Port {
			return errors.New("MGR 需要 MySQL 8.0 和独立的有效通信端口")
		}
		serverIDs := map[uint32]bool{}
		for _, node := range input.Nodes {
			if net.ParseIP(node.LocalIP) == nil || node.ServerID == 0 || serverIDs[node.ServerID] {
				return errors.New("MGR 节点需要唯一 server_id 和有效本机 IP")
			}
			serverIDs[node.ServerID] = true
		}
		if input.MGRAllowlist == "" {
			allowed := make([]string, 0, len(input.Nodes))
			for _, node := range input.Nodes {
				allowed = append(allowed, node.LocalIP)
			}
			input.MGRAllowlist = strings.Join(allowed, ",")
		}
		if input.MGRRecoveryUser == "" {
			input.MGRRecoveryUser = "aim_mgr"
		}
	}
	return nil
}

func (m *JobManager) saveSecret(ctx context.Context, userID int64, name, kind, value string) (int64, error) {
	cipherText, err := m.Secrets.Encrypt([]byte(value))
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := m.Store.DB.ExecContext(ctx, `INSERT INTO secrets(name,kind,cipher_text,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?)`, name, kind, cipherText, userID, now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (m *JobManager) readSecret(ctx context.Context, id int64) (string, error) {
	if id == 0 {
		return "", nil
	}
	var cipherText string
	if err := m.Store.DB.QueryRowContext(ctx, `SELECT cipher_text FROM secrets WHERE id=?`, id).Scan(&cipherText); err != nil {
		return "", err
	}
	plain, err := m.Secrets.Decrypt(cipherText)
	return string(plain), err
}

func (m *JobManager) runDeployment(jobID string, deployment DeploymentRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
	defer cancel()
	defer m.releaseLocks(jobID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := m.transitionJob(jobID, "preflight"); err != nil {
		m.fail(jobID, err)
		return
	}
	_, _ = m.Store.DB.Exec(`UPDATE jobs SET started_at=? WHERE id=?`, now, jobID)
	m.log(jobID, "info", "preflight", "开始检查所有目标主机")
	completedHosts := map[int64]bool{}
	rows, err := m.Store.DB.Query(`SELECT host_id FROM job_hosts WHERE job_id=? AND state='complete'`, jobID)
	if err != nil {
		m.fail(jobID, err)
		return
	}
	for rows.Next() {
		var hostID int64
		if rows.Scan(&hostID) == nil {
			completedHosts[hostID] = true
		}
	}
	_ = rows.Close()

	targets := make([]deploymentTarget, 0, len(deployment.Nodes))
	for _, node := range deployment.Nodes {
		host, key, err := m.SSH.LoadHost(ctx, node.HostID)
		if err != nil {
			m.fail(jobID, err)
			return
		}
		probePorts := []int{deployment.Port}
		if deployment.Mode == "mgr" {
			probePorts = append(probePorts, deployment.MGRPort)
		}
		facts, err := m.SSH.Probe(ctx, node.HostID, probePorts)
		if err != nil {
			m.fail(jobID, fmt.Errorf("主机 %s 预检失败: %w", host.Name, err))
			return
		}
		if completedHosts[node.HostID] && facts.Ports[deployment.Port] != "listening" {
			m.fail(jobID, fmt.Errorf("已完成主机 %s 的端口 %d 不再监听，拒绝盲目恢复", host.Name, deployment.Port))
			return
		}
		if completedHosts[node.HostID] && deployment.Mode == "mgr" && facts.Ports[deployment.MGRPort] != "listening" {
			m.fail(jobID, fmt.Errorf("已完成 MGR 主机 %s 的 XCom 端口 %d 不再监听，拒绝继续 join", host.Name, deployment.MGRPort))
			return
		}
		if !completedHosts[node.HostID] && facts.Ports[deployment.Port] == "listening" {
			m.fail(jobID, fmt.Errorf("主机 %s 的端口 %d 已被占用", host.Name, deployment.Port))
			return
		}
		if deployment.Mode == "mgr" && !contains(facts.IPv4, node.LocalIP) {
			m.fail(jobID, fmt.Errorf("主机 %s 不拥有 MGR 本机 IP %s", host.Name, node.LocalIP))
			return
		}
		root, err := m.readSecret(ctx, node.RootSecret)
		if err != nil {
			m.fail(jobID, err)
			return
		}
		targets = append(targets, deploymentTarget{node: node, host: host, privateKey: key, root: root, facts: facts})
	}

	var media *Media
	if deployment.MediaID != 0 {
		loaded, err := m.loadMedia(ctx, deployment.MediaID)
		if err != nil {
			m.fail(jobID, err)
			return
		}
		if loaded.Version != deployment.Version {
			m.fail(jobID, errors.New("所选安装包版本与部署版本不一致"))
			return
		}
		for _, target := range targets {
			if err := mediaCompatible(loaded, target.facts); err != nil {
				m.fail(jobID, fmt.Errorf("主机 %s: %w", target.host.Name, err))
				return
			}
		}
		media = &loaded
		if err := m.transitionJob(jobID, "transferring"); err != nil {
			m.fail(jobID, err)
			return
		}
		for _, target := range targets {
			if completedHosts[target.host.ID] {
				continue
			}
			m.log(jobID, "info", "transfer", "向 "+target.host.Name+" 分发安装包")
			if err := m.SSH.UploadArchive(ctx, target.host, target.privateKey, jobID, loaded, nil); err != nil {
				m.fail(jobID, fmt.Errorf("向 %s 分发安装包失败: %w", target.host.Name, err))
				return
			}
		}
	}

	if err := m.transitionJob(jobID, "running"); err != nil {
		m.fail(jobID, err)
		return
	}
	replSecret, _ := m.readSecret(ctx, deployment.ReplicationSecretID)
	sourceSecret, _ := m.readSecret(ctx, deployment.SourceSecretID)
	mgrSecret, _ := m.readSecret(ctx, deployment.MGRRecoverySecretID)
	seeds := make([]string, 0, len(targets))
	for _, target := range targets {
		if deployment.Mode == "mgr" {
			seeds = append(seeds, net.JoinHostPort(target.node.LocalIP, strconv.Itoa(deployment.MGRPort)))
		}
	}

	for index, target := range targets {
		if completedHosts[target.host.ID] {
			m.log(jobID, "info", "resume", "跳过已完成节点 "+target.host.Name)
			continue
		}
		role := deployment.Mode
		if deployment.Mode == "replication" {
			if index == 0 {
				role = "source"
			} else {
				role = "replica"
			}
		}
		req := executor.Request{
			Protocol: executor.ProtocolVersion, RequestID: jobID, Action: "install", Version: deployment.Version,
			Port: deployment.Port, Role: role, BindAddress: deployment.BindAddress, ServerID: target.node.ServerID,
			Secrets: executor.Secrets{RootPassword: target.root, ReplicationPassword: replSecret, SourcePassword: sourceSecret, MGRRecoveryPassword: mgrSecret},
		}
		if media != nil {
			req.Archive = &executor.Archive{
				Name: media.Filename, Size: media.Size, SHA256: media.SHA256, Version: media.Version,
				Glibc: media.Glibc, Architecture: media.Architecture, Minimal: media.Minimal,
			}
		}
		if role == "source" {
			replicaHost := deployment.ReplicaHost
			if deployment.Mode == "replication" {
				replicaHost = targets[1].node.LocalIP
				if replicaHost == "" {
					replicaHost = targets[1].host.Address
				}
			}
			req.Replication = executor.Replication{ReplicaHost: replicaHost, User: deployment.ReplicationUser}
		}
		if role == "replica" {
			sourceHost := deployment.SourceHost
			if deployment.Mode == "replication" {
				sourceHost = targets[0].node.LocalIP
				if sourceHost == "" {
					sourceHost = targets[0].host.Address
				}
				req.Secrets.SourcePassword = replSecret
			}
			req.Replication = executor.Replication{SourceHost: sourceHost, SourcePort: deployment.SourcePort, User: deployment.ReplicationUser}
		}
		if role == "mgr" {
			req.MGR = executor.MGR{LocalAddress: target.node.LocalIP, Port: deployment.MGRPort, Seeds: seeds,
				GroupName: deployment.MGRGroupName, Allowlist: deployment.MGRAllowlist,
				Bootstrap: index == 0, RecoveryUser: deployment.MGRRecoveryUser}
		}
		m.log(jobID, "info", "execute", fmt.Sprintf("在 %s 执行 %s 节点部署", target.host.Name, role))
		if err := m.SSH.RunExecutor(ctx, target.host, target.privateKey, req, func(event executor.Event) {
			if event.Message != "" {
				m.log(jobID, event.Level, event.Phase, target.host.Name+": "+event.Message)
			}
		}); err != nil {
			m.fail(jobID, fmt.Errorf("主机 %s 部署失败: %w", target.host.Name, err))
			return
		}
		_, _ = m.Store.DB.Exec(`UPDATE job_hosts SET state='complete' WHERE job_id=? AND host_id=?`, jobID, target.host.ID)
	}
	if err := m.recordDeployment(ctx, deployment, targets, jobID); err != nil {
		m.fail(jobID, err)
		return
	}
	m.complete(jobID, `{"message":"deployment completed"}`)
}

func (m *JobManager) recordDeployment(ctx context.Context, deployment DeploymentRequest, targets []deploymentTarget, jobID string) error {
	tx, err := m.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var clusterID any
	if deployment.Mode == "mgr" || deployment.Mode == "replication" {
		clusterType := deployment.Mode
		groupName := ""
		if deployment.Mode == "mgr" {
			clusterType = "mgr"
			groupName = deployment.MGRGroupName
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO clusters(name,type,group_name,state,created_at,updated_at) VALUES(?,?,?,?,?,?)`, deployment.Name, clusterType, groupName, "online", now, now)
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		clusterID = id
	}
	for index, target := range targets {
		role := deployment.Mode
		if deployment.Mode == "replication" {
			if index == 0 {
				role = "source"
			} else {
				role = "replica"
			}
		}
		spec := deployment
		specJSON, _ := json.Marshal(spec)
		_, err := tx.ExecContext(ctx, `INSERT INTO instances(host_id,version,port,role,service,state,root_secret_id,cluster_id,spec_json,created_at,updated_at)
            VALUES(?,?,?,?,?,'running',?,?,?,?,?)
            ON CONFLICT(host_id,port) DO UPDATE SET version=excluded.version,role=excluded.role,service=excluded.service,state='running',root_secret_id=excluded.root_secret_id,cluster_id=excluded.cluster_id,spec_json=excluded.spec_json,updated_at=excluded.updated_at`,
			target.host.ID, deployment.Version, deployment.Port, role, fmt.Sprintf("aim-mysql-%d", deployment.Port), target.node.RootSecret, clusterID, string(specJSON), now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (m *JobManager) loadMedia(ctx context.Context, id int64) (Media, error) {
	var media Media
	var minimal int
	err := m.Store.DB.QueryRowContext(ctx, `SELECT id,filename,path,size,sha256,version,glibc,architecture,minimal,format,created_at FROM media WHERE id=?`, id).
		Scan(&media.ID, &media.Filename, &media.Path, &media.Size, &media.SHA256, &media.Version, &media.Glibc, &media.Architecture, &minimal, &media.Format, &media.CreatedAt)
	media.Minimal = minimal == 1
	return media, err
}

func mediaCompatible(media Media, facts executor.HostFacts) error {
	arch := facts.Architecture
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	if arch != media.Architecture {
		return fmt.Errorf("安装包架构 %s 与主机架构 %s 不兼容", media.Architecture, facts.Architecture)
	}
	if facts.Glibc != "" && compareVersion(facts.Glibc, media.Glibc) < 0 {
		return fmt.Errorf("安装包需要 glibc %s，主机为 %s", media.Glibc, facts.Glibc)
	}
	return nil
}

func compareVersion(a, b string) int {
	parse := func(value string) []int {
		parts := strings.Split(value, ".")
		result := make([]int, len(parts))
		for i, part := range parts {
			result[i], _ = strconv.Atoi(part)
		}
		return result
	}
	aa, bb := parse(a), parse(b)
	for len(aa) < len(bb) {
		aa = append(aa, 0)
	}
	for len(bb) < len(aa) {
		bb = append(bb, 0)
	}
	for i := range aa {
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *JobManager) log(jobID, level, phase, message string) {
	_, _ = m.Store.DB.Exec(`INSERT INTO job_logs(job_id,created_at,level,phase,message) VALUES(?,?,?,?,?)`, jobID, time.Now().UTC().Format(time.RFC3339Nano), level, phase, message)
}

func (m *JobManager) fail(jobID string, err error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	m.log(jobID, "error", "complete", err.Error())
	if transitionErr := m.transitionJob(jobID, "failed"); transitionErr != nil {
		_, _ = m.Store.DB.Exec(`UPDATE jobs SET error=?,completed_at=? WHERE id=?`, err.Error()+"; "+transitionErr.Error(), now, jobID)
		return
	}
	_, _ = m.Store.DB.Exec(`UPDATE jobs SET error=?,completed_at=? WHERE id=?`, err.Error(), now, jobID)
}

func (m *JobManager) complete(jobID, result string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	m.log(jobID, "info", "complete", "任务执行完成")
	if err := m.transitionJob(jobID, "complete"); err != nil {
		m.fail(jobID, err)
		return
	}
	_, _ = m.Store.DB.Exec(`UPDATE jobs SET result_json=?,completed_at=? WHERE id=?`, result, now, jobID)
}

func canTransitionJobState(current, next string) bool {
	return jobStateTransitions[current][next]
}

func (m *JobManager) transitionJob(jobID, next string) error {
	var current string
	if err := m.Store.DB.QueryRow(`SELECT state FROM jobs WHERE id=?`, jobID).Scan(&current); err != nil {
		return err
	}
	if !canTransitionJobState(current, next) {
		return fmt.Errorf("invalid job state transition %s -> %s", current, next)
	}
	result, err := m.Store.DB.Exec(`UPDATE jobs SET state=? WHERE id=? AND state=?`, next, jobID, current)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("job state changed concurrently")
	}
	return nil
}

func (m *JobManager) releaseLocks(jobID string) {
	_, _ = m.Store.DB.Exec(`DELETE FROM host_locks WHERE job_id=?`, jobID)
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}
