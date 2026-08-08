package console

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aimdotsh/aim/internal/executor"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

var safeRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

type SSHManager struct {
	Store             *Store
	Secrets           *SecretBox
	ConnectTimeout    time.Duration
	RemoteStagingRoot string
}

func (m *SSHManager) LoadHost(ctx context.Context, id int64) (Host, []byte, error) {
	var host Host
	var factsJSON string
	var lastSeen sql.NullString
	err := m.Store.DB.QueryRowContext(ctx, `SELECT id,name,address,ssh_port,ssh_user,private_key_cipher,host_key_fingerprint,facts_json,status,last_error,last_seen_at FROM hosts WHERE id=?`, id).
		Scan(&host.ID, &host.Name, &host.Address, &host.SSHPort, &host.SSHUser, &host.PrivateKeyCipher, &host.HostKeyFingerprint, &factsJSON, &host.Status, &host.LastError, &lastSeen)
	if err != nil {
		return Host{}, nil, err
	}
	host.Facts = map[string]any{}
	_ = json.Unmarshal([]byte(factsJSON), &host.Facts)
	host.LastSeenAt = scanNullableTime(lastSeen)
	privateKey, err := m.Secrets.Decrypt(host.PrivateKeyCipher)
	return host, privateKey, err
}

func (m *SSHManager) ScanFingerprint(ctx context.Context, host Host) (string, error) {
	address := net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))
	conn, err := (&net.Dialer{Timeout: m.ConnectTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	var fingerprint string
	captureErr := errors.New("fingerprint captured")
	config := &ssh.ClientConfig{
		User:    host.SSHUser,
		Timeout: m.ConnectTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return captureErr
		},
	}
	_, _, _, err = ssh.NewClientConn(conn, address, config)
	if fingerprint == "" {
		return "", fmt.Errorf("SSH handshake failed before host key was received: %w", err)
	}
	return fingerprint, nil
}

func (m *SSHManager) dial(ctx context.Context, host Host, privateKey []byte) (*ssh.Client, error) {
	if host.HostKeyFingerprint == "" {
		return nil, errors.New("SSH host fingerprint has not been confirmed")
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("parse SSH private key: %w", err)
	}
	address := net.JoinHostPort(host.Address, strconv.Itoa(host.SSHPort))
	netConn, err := (&net.Dialer{Timeout: m.ConnectTimeout}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: host.SSHUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if actual != host.HostKeyFingerprint {
				return fmt.Errorf("SSH host key changed: expected %s, got %s", host.HostKeyFingerprint, actual)
			}
			return nil
		},
		Timeout: m.ConnectTimeout,
	}
	clientConn, channels, requests, err := ssh.NewClientConn(netConn, address, config)
	if err != nil {
		netConn.Close()
		return nil, err
	}
	return ssh.NewClient(clientConn, channels, requests), nil
}

func (m *SSHManager) Probe(ctx context.Context, hostID int64, ports []int) (executor.HostFacts, error) {
	host, privateKey, err := m.LoadHost(ctx, hostID)
	if err != nil {
		return executor.HostFacts{}, err
	}
	req := executor.Request{Protocol: executor.ProtocolVersion, RequestID: "probe-" + strconv.FormatInt(time.Now().UnixNano(), 10), Action: "probe", ProbePorts: ports}
	var facts executor.HostFacts
	err = m.RunExecutor(ctx, host, privateKey, req, func(event executor.Event) {
		if event.Facts != nil {
			facts = *event.Facts
		}
	})
	if err != nil {
		return executor.HostFacts{}, err
	}
	b, _ := json.Marshal(facts)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = m.Store.DB.ExecContext(ctx, `UPDATE hosts SET facts_json=?,status='online',last_error='',last_seen_at=?,updated_at=? WHERE id=?`, string(b), now, now, hostID)
	return facts, nil
}

func (m *SSHManager) RunExecutor(ctx context.Context, host Host, privateKey []byte, req executor.Request, onEvent func(executor.Event)) error {
	client, err := m.dial(ctx, host, privateKey)
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return err
	}
	if err := session.Start("sudo -n /usr/local/sbin/aim-executor"); err != nil {
		return err
	}
	go func() {
		_ = json.NewEncoder(stdin).Encode(req)
		_ = stdin.Close()
	}()
	stderrDone := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(stderr, 1<<20))
		stderrDone <- strings.TrimSpace(string(b))
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		var event executor.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("invalid executor event: %w", err)
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	waitErr := session.Wait()
	stderrText := <-stderrDone
	if waitErr != nil {
		if stderrText != "" {
			return fmt.Errorf("executor failed: %s", stderrText)
		}
		return waitErr
	}
	return nil
}

func (m *SSHManager) UploadArchive(ctx context.Context, host Host, privateKey []byte, requestID string, media Media, progress func(int64, int64)) error {
	if !safeRequestIDPattern.MatchString(requestID) {
		return errors.New("invalid request ID for remote staging")
	}
	client, err := m.dial(ctx, host, privateKey)
	if err != nil {
		return err
	}
	defer client.Close()
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sftpClient.Close()
	remoteDir := filepath.ToSlash(filepath.Join(m.RemoteStagingRoot, requestID))
	if err := sftpClient.MkdirAll(remoteDir); err != nil {
		return err
	}
	remotePath := filepath.ToSlash(filepath.Join(remoteDir, media.Filename))
	local, err := os.Open(media.Path)
	if err != nil {
		return err
	}
	defer local.Close()
	var offset int64
	if info, err := sftpClient.Stat(remotePath); err == nil {
		if info.Size() > media.Size {
			if err := sftpClient.Remove(remotePath); err != nil {
				return err
			}
		} else {
			offset = info.Size()
		}
	}
	remote, err := sftpClient.OpenFile(remotePath, os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer remote.Close()
	if _, err := local.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := remote.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, 1<<20)
	written := offset
	if progress != nil {
		progress(written, media.Size)
	}
	for {
		n, readErr := local.Read(buffer)
		if n > 0 {
			if _, err := remote.Write(buffer[:n]); err != nil {
				return err
			}
			written += int64(n)
			if progress != nil {
				progress(written, media.Size)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func (m *SSHManager) DownloadStagedFile(ctx context.Context, host Host, privateKey []byte, requestID, remotePath, localPath string, expectedSize int64, progress func(int64, int64)) error {
	if err := validateStagedPath(requestID, remotePath, m.RemoteStagingRoot); err != nil {
		return err
	}
	if filepath.IsAbs(localPath) == false || filepath.Base(localPath) == "." || filepath.Base(localPath) == ".." {
		return errors.New("invalid local backup path")
	}
	client, err := m.dial(ctx, host, privateKey)
	if err != nil {
		return err
	}
	defer client.Close()
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sftpClient.Close()
	info, err := sftpClient.Stat(remotePath)
	if err != nil {
		return fmt.Errorf("远程备份文件不存在: %w", err)
	}
	if !info.Mode().IsRegular() || (expectedSize > 0 && info.Size() != expectedSize) {
		return errors.New("远程备份文件大小或类型校验失败")
	}
	remote, err := sftpClient.Open(remotePath)
	if err != nil {
		return err
	}
	defer remote.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		return err
	}
	partial := localPath + ".partial"
	_ = os.Remove(partial)
	local, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var copied int64
	if progress != nil {
		progress(0, info.Size())
	}
	buffer := make([]byte, 1<<20)
	for {
		n, readErr := remote.Read(buffer)
		if n > 0 {
			if _, err := local.Write(buffer[:n]); err != nil {
				_ = local.Close()
				_ = os.Remove(partial)
				return err
			}
			copied += int64(n)
			if progress != nil {
				progress(copied, info.Size())
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = local.Close()
			_ = os.Remove(partial)
			return readErr
		}
		select {
		case <-ctx.Done():
			_ = local.Close()
			_ = os.Remove(partial)
			return ctx.Err()
		default:
		}
	}
	if err := local.Close(); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if copied != info.Size() {
		_ = os.Remove(partial)
		return errors.New("下载的备份文件大小不一致")
	}
	return os.Rename(partial, localPath)
}

func (m *SSHManager) RemoveStagedFile(ctx context.Context, host Host, privateKey []byte, requestID, remotePath string) error {
	if err := validateStagedPath(requestID, remotePath, m.RemoteStagingRoot); err != nil {
		return err
	}
	client, err := m.dial(ctx, host, privateKey)
	if err != nil {
		return err
	}
	defer client.Close()
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sftpClient.Close()
	if err := sftpClient.Remove(remotePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	requestDir := filepath.ToSlash(filepath.Join(m.RemoteStagingRoot, requestID))
	_ = sftpClient.Remove(filepath.ToSlash(filepath.Join(requestDir, ".aim-client.cnf")))
	_ = sftpClient.Remove(requestDir)
	return nil
}

func validateStagedPath(requestID, remotePath, stagingRoot string) error {
	if !safeRequestIDPattern.MatchString(requestID) {
		return errors.New("invalid request ID for remote staging")
	}
	clean := filepath.Clean(remotePath)
	root := filepath.Clean(filepath.Join(stagingRoot, requestID))
	if clean != filepath.Join(root, "backup.sql.gz") || !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return errors.New("remote backup path is outside the task staging directory")
	}
	return nil
}
