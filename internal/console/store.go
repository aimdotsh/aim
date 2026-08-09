package console

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{DB: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
            password_hash TEXT NOT NULL, role TEXT NOT NULL CHECK(role IN ('admin','operator','viewer')),
            active INTEGER NOT NULL DEFAULT 1, oidc_subject TEXT, auth_provider TEXT NOT NULL DEFAULT 'local',
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS sessions (
            token_hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            csrf_token TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL,
            remote_addr TEXT NOT NULL, user_agent TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS hosts (
            id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, address TEXT NOT NULL,
            ssh_port INTEGER NOT NULL DEFAULT 22, ssh_user TEXT NOT NULL DEFAULT 'aimops',
            private_key_cipher TEXT NOT NULL, host_key_fingerprint TEXT NOT NULL DEFAULT '',
            facts_json TEXT NOT NULL DEFAULT '{}', status TEXT NOT NULL DEFAULT 'pending',
            last_error TEXT NOT NULL DEFAULT '', last_seen_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS media (
            id INTEGER PRIMARY KEY AUTOINCREMENT, filename TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
            size INTEGER NOT NULL, sha256 TEXT NOT NULL UNIQUE, version TEXT NOT NULL,
            glibc TEXT NOT NULL DEFAULT '', architecture TEXT NOT NULL, minimal INTEGER NOT NULL DEFAULT 0,
            format TEXT NOT NULL, created_by INTEGER REFERENCES users(id), created_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS uploads (
            id TEXT PRIMARY KEY, filename TEXT NOT NULL, path TEXT NOT NULL, expected_size INTEGER NOT NULL,
            received_size INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id),
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS upload_chunks (
            upload_id TEXT NOT NULL REFERENCES uploads(id) ON DELETE CASCADE, chunk_index INTEGER NOT NULL,
            size INTEGER NOT NULL, PRIMARY KEY(upload_id, chunk_index)
        )`,
		`CREATE TABLE IF NOT EXISTS jobs (
            id TEXT PRIMARY KEY, kind TEXT NOT NULL, state TEXT NOT NULL,
            payload_json TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '',
            created_by INTEGER NOT NULL REFERENCES users(id), created_at TEXT NOT NULL,
            started_at TEXT, completed_at TEXT, confirmation_hash TEXT NOT NULL DEFAULT '', preview_expires_at TEXT
        )`,
		`CREATE TABLE IF NOT EXISTS job_hosts (
            job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
            host_id INTEGER NOT NULL REFERENCES hosts(id), step_order INTEGER NOT NULL,
            state TEXT NOT NULL DEFAULT 'queued', PRIMARY KEY(job_id, host_id)
        )`,
		`CREATE TABLE IF NOT EXISTS host_locks (
            host_id INTEGER PRIMARY KEY REFERENCES hosts(id), job_id TEXT NOT NULL REFERENCES jobs(id), acquired_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS job_logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
            created_at TEXT NOT NULL, level TEXT NOT NULL, phase TEXT NOT NULL, message TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS instances (
            id INTEGER PRIMARY KEY AUTOINCREMENT, host_id INTEGER NOT NULL REFERENCES hosts(id),
            version TEXT NOT NULL, port INTEGER NOT NULL, role TEXT NOT NULL, service TEXT NOT NULL,
            state TEXT NOT NULL, root_secret_id INTEGER, cluster_id INTEGER,
            spec_json TEXT NOT NULL DEFAULT '{}',
            created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(host_id, port)
        )`,
		`CREATE TABLE IF NOT EXISTS clusters (
            id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL,
            group_name TEXT NOT NULL DEFAULT '', state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS secrets (
            id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, kind TEXT NOT NULL,
            cipher_text TEXT NOT NULL, created_by INTEGER REFERENCES users(id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS audit_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER REFERENCES users(id), username TEXT NOT NULL,
            remote_addr TEXT NOT NULL, action TEXT NOT NULL, object_type TEXT NOT NULL,
            object_id TEXT NOT NULL, detail_json TEXT NOT NULL, created_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS backup_plans (
            id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, instance_id INTEGER NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
            owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, schedule TEXT NOT NULL,
            all_databases INTEGER NOT NULL DEFAULT 1, databases_json TEXT NOT NULL DEFAULT '[]',
            retention_days INTEGER NOT NULL DEFAULT 30, retention_count INTEGER NOT NULL DEFAULT 30,
            enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE IF NOT EXISTS backup_runs (
            id TEXT PRIMARY KEY, plan_id INTEGER NOT NULL REFERENCES backup_plans(id) ON DELETE CASCADE,
            instance_id INTEGER NOT NULL REFERENCES instances(id) ON DELETE CASCADE, owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
            status TEXT NOT NULL, file_name TEXT NOT NULL DEFAULT '', file_path TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
            sha256 TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT ''
        )`,
		`CREATE TABLE IF NOT EXISTS monitor_samples (
            id INTEGER PRIMARY KEY AUTOINCREMENT, instance_id INTEGER NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
            collected_at TEXT NOT NULL, metrics_json TEXT NOT NULL
        )`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_job_logs_job_id ON job_logs(job_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_backup_runs_started ON backup_runs(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_monitor_samples_instance ON monitor_samples(instance_id,collected_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	if err := s.ensureUserOIDCColumns(ctx); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oidc_subject ON users(oidc_subject) WHERE oidc_subject IS NOT NULL`); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	// A controller restart must never blindly replay an in-flight remote mutation.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET state='failed', error='controller restarted before cleanup task started', completed_at=? WHERE kind='deployment_cleanup' AND state='queued'`, now); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE jobs SET state='needs_verification', error='controller restarted while task was running', completed_at=? WHERE state IN ('running','preflight','transferring')`, now); err != nil {
		return err
	}
	// A completed non-preview cleanup is the durable proof that its failed
	// deployment has already been cleaned. Backfill this relation for tasks
	// created by releases that did not persist the source lifecycle state.
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE jobs AS source
		SET state='cleaned'
		WHERE source.kind='deployment'
		  AND source.state IN ('failed','cleanup_running')
		  AND EXISTS (
			SELECT 1 FROM jobs AS cleanup
			WHERE cleanup.kind='deployment_cleanup'
			  AND cleanup.state='complete'
			  AND CASE WHEN json_valid(cleanup.payload_json) THEN json_extract(cleanup.payload_json,'$.source_job_id') END=source.id
			  AND COALESCE(CASE WHEN json_valid(cleanup.payload_json) THEN json_extract(cleanup.payload_json,'$.dry_run') END,0)=0
		  )`); err != nil {
		return err
	}
	// Interrupted real cleanup is safe to retry: the restricted cleanup command
	// is idempotent, while silently replaying it after a restart is not allowed.
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE jobs AS source
		SET state='failed'
		WHERE source.kind='deployment'
		  AND source.state='cleanup_running'
		  AND EXISTS (
			SELECT 1 FROM jobs AS cleanup
			WHERE cleanup.kind='deployment_cleanup'
			  AND cleanup.state IN ('failed','needs_verification')
			  AND CASE WHEN json_valid(cleanup.payload_json) THEN json_extract(cleanup.payload_json,'$.source_job_id') END=source.id
			  AND COALESCE(CASE WHEN json_valid(cleanup.payload_json) THEN json_extract(cleanup.payload_json,'$.dry_run') END,0)=0
		  )`); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM host_locks`)
	return err
}

func (s *Store) ensureUserOIDCColumns(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `PRAGMA table_info(users)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		columns[name] = true
	}
	if !columns["oidc_subject"] {
		if _, err := s.DB.ExecContext(ctx, `ALTER TABLE users ADD COLUMN oidc_subject TEXT`); err != nil {
			return err
		}
	}
	if !columns["auth_provider"] {
		if _, err := s.DB.ExecContext(ctx, `ALTER TABLE users ADD COLUMN auth_provider TEXT NOT NULL DEFAULT 'local'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertOIDCUser(ctx context.Context, subject, preferredUsername, role string) (*User, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("OIDC subject is required")
	}
	if role != "admin" && role != "operator" && role != "viewer" {
		return nil, errors.New("invalid OIDC role")
	}
	var user User
	var active int
	err := s.DB.QueryRowContext(ctx, `SELECT id,username,role,active FROM users WHERE oidc_subject=?`, subject).
		Scan(&user.ID, &user.Username, &user.Role, &active)
	if err == nil {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := s.DB.ExecContext(ctx, `UPDATE users SET role=?,active=1,updated_at=? WHERE id=?`, role, now, user.ID); err != nil {
			return nil, err
		}
		user.Role, user.Active = role, true
		return &user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	username := sanitizeOIDCUsername(preferredUsername)
	var exists int
	if s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username=?`, username).Scan(&exists) != nil || exists > 0 {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(subject)))
		username += "-" + digest[:8]
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,active,oidc_subject,auth_provider,created_at,updated_at) VALUES(?, '!oidc', ?, 1, ?, 'oidc', ?, ?)`, username, role, subject, now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Role: role, Active: true}, nil
}

func sanitizeOIDCUsername(value string) string {
	value = strings.TrimSpace(value)
	var result strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-@", r) {
			result.WriteRune(r)
		}
	}
	if result.Len() == 0 {
		return "lazycat-user"
	}
	return result.String()
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, password string) (bool, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	if username == "" || password == "" {
		return false, errors.New("AIM_ADMIN_USER and AIM_ADMIN_PASSWORD are required for first startup")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES(?,?,'admin',?,?)`, username, hash, now, now)
	return err == nil, err
}

func (s *Store) Audit(ctx context.Context, user *User, remoteAddr, action, objectType, objectID, detail string) {
	var userID any
	username := "system"
	if user != nil {
		userID = user.ID
		username = user.Username
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO audit_events(user_id,username,remote_addr,action,object_type,object_id,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		userID, username, remoteAddr, action, objectType, objectID, detail, time.Now().UTC().Format(time.RFC3339Nano))
}

func (s *Store) Close() error { return s.DB.Close() }
