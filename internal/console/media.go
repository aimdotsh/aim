package console

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const UploadChunkSize int64 = 16 << 20

var mediaPattern = regexp.MustCompile(`^mysql-((?:5\.6|5\.7|8\.0|8\.4)\.[0-9]+)-linux-glibc([0-9]+\.[0-9]+)-(x86_64|aarch64|i686)(-minimal)?\.(tar\.xz|tar\.gz|tgz|tar)$`)

type MediaMetadata struct {
	Version      string
	Glibc        string
	Architecture string
	Minimal      bool
	Format       string
}

func ParseMediaFilename(filename string) (MediaMetadata, error) {
	if filename != filepath.Base(filename) || strings.HasPrefix(filename, "mysql-test-") {
		return MediaMetadata{}, errors.New("不是可安装的 MySQL Server 软件包")
	}
	matches := mediaPattern.FindStringSubmatch(filename)
	if matches == nil {
		return MediaMetadata{}, errors.New("软件包名称、版本、架构或压缩格式不受支持")
	}
	return MediaMetadata{Version: matches[1], Glibc: matches[2], Architecture: matches[3], Minimal: matches[4] != "", Format: matches[5]}, nil
}

type UploadManager struct {
	Store     *Store
	Root      string
	MaxSize   int64
	MediaRoot string
}

func (m *UploadManager) Create(userID int64, filename string, size int64) (string, error) {
	if _, err := ParseMediaFilename(filename); err != nil {
		return "", err
	}
	if size <= 0 || size > m.MaxSize {
		return "", fmt.Errorf("软件包大小必须在 1 字节到 %d 字节之间", m.MaxSize)
	}
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.Root, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(m.Root, id+".part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = m.Store.DB.Exec(`INSERT INTO uploads(id,filename,path,expected_size,status,created_by,created_at,updated_at) VALUES(?,?,?,?,'uploading',?,?,?)`, id, filename, path, size, userID, now, now)
	return id, err
}

func (m *UploadManager) WriteChunk(id string, index int, body io.Reader, contentLength int64) error {
	if index < 0 || contentLength <= 0 || contentLength > UploadChunkSize {
		return errors.New("分块索引或大小无效")
	}
	var path, status string
	var expectedSize int64
	if err := m.Store.DB.QueryRow(`SELECT path,expected_size,status FROM uploads WHERE id=?`, id).Scan(&path, &expectedSize, &status); err != nil {
		return err
	}
	if status != "uploading" {
		return errors.New("上传任务不再接受数据")
	}
	offset := int64(index) * UploadChunkSize
	if offset >= expectedSize || offset+contentLength > expectedSize {
		return errors.New("分块超出声明的文件大小")
	}
	if offset+contentLength < expectedSize && contentLength != UploadChunkSize {
		return errors.New("非末尾分块必须为 16 MiB")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	section := io.NewOffsetWriter(f, offset)
	written, err := io.CopyN(section, body, contentLength)
	if err != nil || written != contentLength {
		return errors.New("分块内容长度与声明不一致")
	}
	tx, err := m.Store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous int64
	err = tx.QueryRow(`SELECT size FROM upload_chunks WHERE upload_id=? AND chunk_index=?`, id, index).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO upload_chunks(upload_id,chunk_index,size) VALUES(?,?,?) ON CONFLICT(upload_id,chunk_index) DO UPDATE SET size=excluded.size`, id, index, contentLength); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE uploads SET received_size=received_size-?+?,updated_at=? WHERE id=?`, previous, contentLength, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *UploadManager) Complete(id string, userID int64) (Media, error) {
	var filename, path, status string
	var expectedSize, receivedSize int64
	if err := m.Store.DB.QueryRow(`SELECT filename,path,expected_size,received_size,status FROM uploads WHERE id=?`, id).
		Scan(&filename, &path, &expectedSize, &receivedSize, &status); err != nil {
		return Media{}, err
	}
	if status != "uploading" || expectedSize != receivedSize {
		return Media{}, errors.New("文件分块尚未完整上传")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		return Media{}, errors.New("暂存文件大小不匹配")
	}
	f, err := os.Open(path)
	if err != nil {
		return Media{}, err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr := f.Close()
	if copyErr != nil {
		return Media{}, copyErr
	}
	if closeErr != nil {
		return Media{}, closeErr
	}
	digest := hex.EncodeToString(h.Sum(nil))
	metadata, err := ParseMediaFilename(filename)
	if err != nil {
		return Media{}, err
	}
	if err := os.MkdirAll(m.MediaRoot, 0o750); err != nil {
		return Media{}, err
	}
	destinationDir := filepath.Join(m.MediaRoot, digest)
	if err := os.MkdirAll(destinationDir, 0o750); err != nil {
		return Media{}, err
	}
	destination := filepath.Join(destinationDir, filename)
	if err := os.Rename(path, destination); err != nil {
		return Media{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := m.Store.DB.Exec(`INSERT INTO media(filename,path,size,sha256,version,glibc,architecture,minimal,format,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		filename, destination, expectedSize, digest, metadata.Version, metadata.Glibc, metadata.Architecture, metadata.Minimal, metadata.Format, userID, now)
	if err != nil {
		return Media{}, err
	}
	mediaID, _ := result.LastInsertId()
	_, _ = m.Store.DB.Exec(`UPDATE uploads SET status='complete',updated_at=? WHERE id=?`, now, id)
	return Media{ID: mediaID, Filename: filename, Path: destination, Size: expectedSize, SHA256: digest, Version: metadata.Version, Glibc: metadata.Glibc, Architecture: metadata.Architecture, Minimal: metadata.Minimal, Format: metadata.Format, CreatedAt: now}, nil
}

func ParsePositiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, errors.New("invalid non-negative integer")
	}
	return n, nil
}
