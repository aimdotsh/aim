package console

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aimdotsh/aim/internal/webui"
)

type ServerConfig struct {
	CookieSecure bool
	UploadRoot   string
	MediaRoot    string
	MaxUpload    int64
}

type Server struct {
	Store   *Store
	Secrets *SecretBox
	Auth    *Auth
	Uploads *UploadManager
	SSH     *SSHManager
	Jobs    *JobManager
	Handler http.Handler
}

func NewServer(store *Store, secrets *SecretBox, config ServerConfig) (*Server, error) {
	auth := NewAuth(store, config.CookieSecure)
	sshManager := &SSHManager{Store: store, Secrets: secrets, ConnectTimeout: 10 * time.Second, RemoteStagingRoot: "/var/lib/aim-staging"}
	server := &Server{
		Store: store, Secrets: secrets, Auth: auth,
		Uploads: &UploadManager{Store: store, Root: config.UploadRoot, MediaRoot: config.MediaRoot, MaxSize: config.MaxUpload},
		SSH:     sshManager,
	}
	server.Jobs = &JobManager{Store: store, Secrets: secrets, SSH: sshManager}
	server.Handler = server.routes()
	return server, nil
}

func (s *Server) routes() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.HandleFunc("POST /api/v1/session", s.Auth.Login)

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/session", s.Auth.Current)
	api.HandleFunc("DELETE /api/v1/session", s.Auth.Logout)
	api.HandleFunc("GET /api/v1/dashboard", s.dashboard)
	api.Handle("GET /api/v1/hosts", RequireRole("admin", "operator", "viewer")(http.HandlerFunc(s.listHosts)))
	api.Handle("POST /api/v1/hosts", RequireRole("admin")(http.HandlerFunc(s.createHost)))
	api.Handle("POST /api/v1/hosts/{id}/fingerprint", RequireRole("admin")(http.HandlerFunc(s.hostFingerprint)))
	api.Handle("POST /api/v1/hosts/{id}/probe", RequireRole("admin", "operator")(http.HandlerFunc(s.probeHost)))
	api.Handle("GET /api/v1/media", RequireRole("admin", "operator", "viewer")(http.HandlerFunc(s.listMedia)))
	api.Handle("POST /api/v1/media/uploads", RequireRole("admin", "operator")(http.HandlerFunc(s.createUpload)))
	api.Handle("GET /api/v1/media/uploads/{id}", RequireRole("admin", "operator")(http.HandlerFunc(s.getUpload)))
	api.Handle("PUT /api/v1/media/uploads/{id}/chunks/{index}", RequireRole("admin", "operator")(http.HandlerFunc(s.writeUploadChunk)))
	api.Handle("POST /api/v1/media/uploads/{id}/complete", RequireRole("admin", "operator")(http.HandlerFunc(s.completeUpload)))
	api.Handle("POST /api/v1/deployments", RequireRole("admin", "operator")(http.HandlerFunc(s.createDeployment)))
	api.HandleFunc("GET /api/v1/jobs", s.listJobs)
	api.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	api.HandleFunc("GET /api/v1/jobs/{id}/events", s.jobEvents)
	api.Handle("POST /api/v1/jobs/{id}/retry", RequireRole("admin", "operator")(http.HandlerFunc(s.retryJob)))
	api.Handle("POST /api/v1/jobs/{id}/verify", RequireRole("admin", "operator")(http.HandlerFunc(s.verifyJob)))
	api.HandleFunc("GET /api/v1/instances", s.listInstances)
	api.Handle("POST /api/v1/instances/{id}/actions", RequireRole("admin", "operator")(http.HandlerFunc(s.instanceAction)))
	api.HandleFunc("GET /api/v1/clusters", s.listClusters)
	api.Handle("GET /api/v1/users", RequireRole("admin")(http.HandlerFunc(s.listUsers)))
	api.Handle("POST /api/v1/users", RequireRole("admin")(http.HandlerFunc(s.createUser)))
	api.Handle("PATCH /api/v1/users/{id}", RequireRole("admin")(http.HandlerFunc(s.updateUser)))
	api.Handle("GET /api/v1/audit", RequireRole("admin")(http.HandlerFunc(s.listAudit)))
	api.Handle("GET /api/v1/secrets", RequireRole("admin")(http.HandlerFunc(s.listSecrets)))
	api.Handle("POST /api/v1/secrets/{id}/reveal", RequireRole("admin")(http.HandlerFunc(s.revealSecret)))
	root.Handle("/api/v1/", s.Auth.Middleware(api))
	root.Handle("/", staticHandler())
	return securityHeaders(root)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func staticHandler() http.Handler {
	dist, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if path == "." || path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err != nil {
			r.URL.Path = "/"
			path = "index.html"
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-store")
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	result := map[string]int{}
	for name, query := range map[string]string{
		"hosts": "SELECT COUNT(*) FROM hosts", "instances": "SELECT COUNT(*) FROM instances",
		"clusters": "SELECT COUNT(*) FROM clusters", "running_jobs": "SELECT COUNT(*) FROM jobs WHERE state IN ('queued','preflight','transferring','running')",
	} {
		var count int
		_ = s.Store.DB.QueryRowContext(r.Context(), query).Scan(&count)
		result[name] = count
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,name,address,ssh_port,ssh_user,host_key_fingerprint,facts_json,status,last_error,last_seen_at FROM hosts ORDER BY name`)
	if err != nil {
		writeError(w, 500, "读取主机失败")
		return
	}
	defer rows.Close()
	hosts := []Host{}
	for rows.Next() {
		var host Host
		var factsJSON string
		var lastSeen sql.NullString
		if err := rows.Scan(&host.ID, &host.Name, &host.Address, &host.SSHPort, &host.SSHUser, &host.HostKeyFingerprint, &factsJSON, &host.Status, &host.LastError, &lastSeen); err != nil {
			continue
		}
		host.Facts = map[string]any{}
		_ = json.Unmarshal([]byte(factsJSON), &host.Facts)
		host.LastSeenAt = scanNullableTime(lastSeen)
		hosts = append(hosts, host)
	}
	writeJSON(w, 200, hosts)
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name       string `json:"name"`
		Address    string `json:"address"`
		SSHPort    int    `json:"ssh_port"`
		SSHUser    string `json:"ssh_user"`
		PrivateKey string `json:"private_key"`
	}
	if decodeJSON(w, r, &input, 2<<20) != nil {
		return
	}
	if input.Name == "" || (!hostnamePattern.MatchString(input.Address) && net.ParseIP(input.Address) == nil) || input.PrivateKey == "" {
		writeError(w, 400, "主机名称、地址或 SSH 私钥无效")
		return
	}
	if input.SSHPort == 0 {
		input.SSHPort = 22
	}
	if input.SSHPort < 1 || input.SSHPort > 65535 {
		writeError(w, 400, "SSH 端口无效")
		return
	}
	if input.SSHUser == "" {
		input.SSHUser = "aimops"
	}
	cipherText, err := s.Secrets.Encrypt([]byte(input.PrivateKey))
	if err != nil {
		writeError(w, 500, "SSH 私钥加密失败")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.Store.DB.ExecContext(r.Context(), `INSERT INTO hosts(name,address,ssh_port,ssh_user,private_key_cipher,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, input.Name, input.Address, input.SSHPort, input.SSHUser, cipherText, now, now)
	if err != nil {
		writeError(w, 409, "主机名称已存在或数据无效")
		return
	}
	id, _ := result.LastInsertId()
	s.Store.Audit(r.Context(), UserFromContext(r.Context()), remoteIP(r), "host_create", "host", strconv.FormatInt(id, 10), `{}`)
	writeJSON(w, 201, map[string]any{"id": id, "status": "pending"})
}

func (s *Server) hostFingerprint(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, 400, "主机 ID 无效")
		return
	}
	var input struct {
		Confirm string `json:"confirm,omitempty"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	host, _, err := s.SSH.LoadHost(r.Context(), id)
	if err != nil {
		writeError(w, 404, "主机不存在")
		return
	}
	fingerprint, err := s.SSH.ScanFingerprint(r.Context(), host)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	if input.Confirm == "" {
		writeJSON(w, 200, map[string]any{"fingerprint": fingerprint, "confirmed": false})
		return
	}
	if input.Confirm != fingerprint {
		writeError(w, 409, "确认的 SSH 指纹与当前主机不一致")
		return
	}
	_, _ = s.Store.DB.ExecContext(r.Context(), `UPDATE hosts SET host_key_fingerprint=?,status='confirmed',updated_at=? WHERE id=?`, fingerprint, time.Now().UTC().Format(time.RFC3339Nano), id)
	s.Store.Audit(r.Context(), UserFromContext(r.Context()), remoteIP(r), "host_key_confirm", "host", strconv.FormatInt(id, 10), fmt.Sprintf(`{"fingerprint":%q}`, fingerprint))
	writeJSON(w, 200, map[string]any{"fingerprint": fingerprint, "confirmed": true})
}

func (s *Server) probeHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, 400, "主机 ID 无效")
		return
	}
	var input struct {
		Ports []int `json:"ports,omitempty"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	facts, err := s.SSH.Probe(r.Context(), id, input.Ports)
	if err != nil {
		_, _ = s.Store.DB.Exec(`UPDATE hosts SET status='error',last_error=?,updated_at=? WHERE id=?`, err.Error(), time.Now().UTC().Format(time.RFC3339Nano), id)
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, facts)
}

func (s *Server) listMedia(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,filename,size,sha256,version,glibc,architecture,minimal,format,created_at FROM media ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, 500, "读取安装包失败")
		return
	}
	defer rows.Close()
	items := []Media{}
	for rows.Next() {
		var item Media
		var minimal int
		if rows.Scan(&item.ID, &item.Filename, &item.Size, &item.SHA256, &item.Version, &item.Glibc, &item.Architecture, &minimal, &item.Format, &item.CreatedAt) == nil {
			item.Minimal = minimal == 1
			items = append(items, item)
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	id, err := s.Uploads.Create(UserFromContext(r.Context()).ID, input.Filename, input.Size)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "chunk_size": UploadChunkSize})
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	var filename, status string
	var expectedSize, receivedSize int64
	if err := s.Store.DB.QueryRowContext(r.Context(), `SELECT filename,expected_size,received_size,status FROM uploads WHERE id=?`, r.PathValue("id")).
		Scan(&filename, &expectedSize, &receivedSize, &status); err != nil {
		writeError(w, http.StatusNotFound, "上传任务不存在")
		return
	}
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT chunk_index FROM upload_chunks WHERE upload_id=? ORDER BY chunk_index`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取上传进度失败")
		return
	}
	defer rows.Close()
	chunks := []int{}
	for rows.Next() {
		var index int
		if rows.Scan(&index) == nil {
			chunks = append(chunks, index)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": r.PathValue("id"), "filename": filename, "expected_size": expectedSize,
		"received_size": receivedSize, "received_chunks": chunks, "status": status,
		"chunk_size": UploadChunkSize,
	})
}

func (s *Server) writeUploadChunk(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || r.ContentLength < 1 || r.ContentLength > UploadChunkSize {
		writeError(w, 400, "分块索引或大小无效")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, UploadChunkSize)
	if err := s.Uploads.WriteChunk(r.PathValue("id"), index, r.Body, r.ContentLength); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	media, err := s.Uploads.Complete(r.PathValue("id"), UserFromContext(r.Context()).ID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.Store.Audit(r.Context(), UserFromContext(r.Context()), remoteIP(r), "media_upload", "media", strconv.FormatInt(media.ID, 10), fmt.Sprintf(`{"sha256":%q}`, media.SHA256))
	writeJSON(w, 201, media)
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	var input DeploymentRequest
	if decodeJSON(w, r, &input, 256<<10) != nil {
		return
	}
	jobID, err := s.Jobs.CreateDeployment(r.Context(), UserFromContext(r.Context()), remoteIP(r), input)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": jobID})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,kind,state,error,created_at,COALESCE(started_at,''),COALESCE(completed_at,'') FROM jobs ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		writeError(w, 500, "读取任务失败")
		return
	}
	defer rows.Close()
	items := []Job{}
	for rows.Next() {
		var job Job
		if rows.Scan(&job.ID, &job.Kind, &job.State, &job.Error, &job.CreatedAt, &job.StartedAt, &job.CompletedAt) == nil {
			items = append(items, job)
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	var job Job
	err := s.Store.DB.QueryRowContext(r.Context(), `SELECT id,kind,state,result_json,error,created_at,COALESCE(started_at,''),COALESCE(completed_at,'') FROM jobs WHERE id=?`, r.PathValue("id")).
		Scan(&job.ID, &job.Kind, &job.State, &job.ResultJSON, &job.Error, &job.CreatedAt, &job.StartedAt, &job.CompletedAt)
	if err != nil {
		writeError(w, 404, "任务不存在")
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := s.Jobs.RetryDeployment(r.Context(), UserFromContext(r.Context()), remoteIP(r), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (s *Server) verifyJob(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.VerifyInterruptedDeployment(r.Context(), UserFromContext(r.Context()), remoteIP(r), r.PathValue("id")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "verified", "job_id": r.PathValue("id")})
}

func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "服务器不支持实时日志")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,created_at,level,phase,message FROM job_logs WHERE job_id=? AND id>? ORDER BY id`, r.PathValue("id"), lastID)
		if err != nil {
			return
		}
		for rows.Next() {
			var id int64
			var createdAt, level, phase, message string
			if rows.Scan(&id, &createdAt, &level, &phase, &message) == nil {
				payload, _ := json.Marshal(map[string]any{"id": id, "created_at": createdAt, "level": level, "phase": phase, "message": message})
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, payload)
				lastID = id
			}
		}
		rows.Close()
		flusher.Flush()
		var state string
		if s.Store.DB.QueryRowContext(r.Context(), `SELECT state FROM jobs WHERE id=?`, r.PathValue("id")).Scan(&state) != nil {
			return
		}
		if state == "complete" || state == "failed" || state == "needs_verification" {
			fmt.Fprintf(w, "event: complete\ndata: {\"state\":%q}\n\n", state)
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT i.id,i.host_id,h.name,h.address,i.version,i.port,i.role,i.service,i.state,i.cluster_id,i.created_at,i.updated_at FROM instances i JOIN hosts h ON h.id=i.host_id ORDER BY h.name,i.port`)
	if err != nil {
		writeError(w, 500, "读取实例失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, hostID int64
		var name, address, version, role, service, state, created, updated string
		var port int
		var clusterID sql.NullInt64
		if rows.Scan(&id, &hostID, &name, &address, &version, &port, &role, &service, &state, &clusterID, &created, &updated) == nil {
			items = append(items, map[string]any{"id": id, "host_id": hostID, "host_name": name, "address": address, "version": version, "port": port, "role": role, "service": service, "state": state, "cluster_id": clusterID.Int64, "created_at": created, "updated_at": updated})
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) instanceAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, 400, "实例 ID 无效")
		return
	}
	var input InstanceActionInput
	if decodeJSON(w, r, &input, 32<<10) != nil {
		return
	}
	user := UserFromContext(r.Context())
	if (input.Action == "reinitialize" || input.Action == "uninstall") && user.Role != "admin" {
		writeError(w, 403, "只有管理员可以执行破坏性操作")
		return
	}
	jobID, err := s.Jobs.CreateInstanceAction(r.Context(), user, remoteIP(r), id, input)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"job_id": jobID})
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,name,type,group_name,state,created_at,updated_at FROM clusters ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, 500, "读取集群失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, clusterType, groupName, state, created, updated string
		if rows.Scan(&id, &name, &clusterType, &groupName, &state, &created, &updated) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "type": clusterType, "group_name": groupName, "state": state, "created_at": created, "updated_at": updated})
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,username,role,active FROM users ORDER BY username`)
	if err != nil {
		writeError(w, 500, "读取用户失败")
		return
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		var active int
		if rows.Scan(&user.ID, &user.Username, &user.Role, &active) == nil {
			user.Active = active == 1
			users = append(users, user)
		}
	}
	writeJSON(w, 200, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if decodeJSON(w, r, &input, 16<<10) != nil {
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{3,64}$`).MatchString(input.Username) || (input.Role != "admin" && input.Role != "operator" && input.Role != "viewer") {
		writeError(w, 400, "用户名或角色无效")
		return
	}
	hash, err := HashPassword(input.Password)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.Store.DB.ExecContext(r.Context(), `INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES(?,?,?,?,?)`, input.Username, hash, input.Role, now, now)
	if err != nil {
		writeError(w, 409, "用户名已存在")
		return
	}
	id, _ := result.LastInsertId()
	s.Store.Audit(r.Context(), UserFromContext(r.Context()), remoteIP(r), "user_create", "user", strconv.FormatInt(id, 10), fmt.Sprintf(`{"role":%q}`, input.Role))
	writeJSON(w, 201, User{ID: id, Username: input.Username, Role: input.Role, Active: true})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, 400, "用户 ID 无效")
		return
	}
	var input struct {
		Role   string `json:"role"`
		Active bool   `json:"active"`
	}
	if decodeJSON(w, r, &input, 8<<10) != nil {
		return
	}
	if input.Role != "admin" && input.Role != "operator" && input.Role != "viewer" {
		writeError(w, 400, "角色无效")
		return
	}
	current := UserFromContext(r.Context())
	if id == current.ID && (!input.Active || input.Role != "admin") {
		writeError(w, 400, "不能停用当前管理员或移除自己的管理员角色")
		return
	}
	var oldRole, username string
	var oldActive int
	if err := s.Store.DB.QueryRowContext(r.Context(), `SELECT username,role,active FROM users WHERE id=?`, id).Scan(&username, &oldRole, &oldActive); err != nil {
		writeError(w, 404, "用户不存在")
		return
	}
	if oldRole == "admin" && oldActive == 1 && (!input.Active || input.Role != "admin") {
		var admins int
		_ = s.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE role='admin' AND active=1`).Scan(&admins)
		if admins <= 1 {
			writeError(w, 400, "不能停用或降级最后一个管理员")
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.Store.DB.ExecContext(r.Context(), `UPDATE users SET role=?,active=?,updated_at=? WHERE id=?`, input.Role, input.Active, now, id)
	if err != nil {
		writeError(w, 500, "用户更新失败")
		return
	}
	if !input.Active {
		_, _ = s.Store.DB.ExecContext(r.Context(), `DELETE FROM sessions WHERE user_id=?`, id)
	}
	s.Store.Audit(r.Context(), current, remoteIP(r), "user_update", "user", strconv.FormatInt(id, 10), fmt.Sprintf(`{"username":%q,"role":%q,"active":%t}`, username, input.Role, input.Active))
	writeJSON(w, 200, User{ID: id, Username: username, Role: input.Role, Active: input.Active})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,username,remote_addr,action,object_type,object_id,detail_json,created_at FROM audit_events ORDER BY id DESC LIMIT 500`)
	if err != nil {
		writeError(w, 500, "读取审计日志失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var username, remote, action, objectType, objectID, detail, created string
		if rows.Scan(&id, &username, &remote, &action, &objectType, &objectID, &detail, &created) == nil {
			items = append(items, map[string]any{"id": id, "username": username, "remote_addr": remote, "action": action, "object_type": objectType, "object_id": objectID, "detail_json": detail, "created_at": created})
		}
	}
	writeJSON(w, 200, items)
}

func (s *Server) revealSecret(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, 400, "秘密 ID 无效")
		return
	}
	var name, kind, cipherText string
	if err := s.Store.DB.QueryRowContext(r.Context(), `SELECT name,kind,cipher_text FROM secrets WHERE id=?`, id).Scan(&name, &kind, &cipherText); err != nil {
		writeError(w, 404, "秘密不存在")
		return
	}
	plain, err := s.Secrets.Decrypt(cipherText)
	if err != nil {
		writeError(w, 500, "秘密解密失败")
		return
	}
	s.Store.Audit(r.Context(), UserFromContext(r.Context()), remoteIP(r), "secret_reveal", "secret", strconv.FormatInt(id, 10), fmt.Sprintf(`{"kind":%q}`, kind))
	writeJSON(w, 200, map[string]string{"name": name, "kind": kind, "value": string(plain)})
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.DB.QueryContext(r.Context(), `SELECT id,name,kind,created_at,updated_at FROM secrets ORDER BY id DESC LIMIT 500`)
	if err != nil {
		writeError(w, 500, "读取秘密列表失败")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, kind, created, updated string
		if rows.Scan(&id, &name, &kind, &created, &updated) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "kind": kind, "created_at": created, "updated_at": updated})
		}
	}
	writeJSON(w, 200, items)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func ensureDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	return os.MkdirAll(path, 0o750)
}
