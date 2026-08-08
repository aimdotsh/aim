package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aimdotsh/aim/internal/console"
)

func main() {
	dataDir := env("AIM_DATA_DIR", "/var/lib/aim-console")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		log.Fatal(err)
	}
	key, err := loadMasterKey(os.Getenv("AIM_MASTER_KEY"), env("AIM_MASTER_KEY_FILE", "/run/secrets/aim_master_key"))
	if err != nil {
		log.Fatal(err)
	}
	secretBox, err := console.NewSecretBox(key)
	if err != nil {
		log.Fatal(err)
	}
	store, err := console.OpenStore(filepath.Join(dataDir, "aim.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	localAuth := strings.EqualFold(env("AIM_LOCAL_AUTH_ENABLED", "true"), "true")
	if localAuth {
		created, err := store.BootstrapAdmin(context.Background(), os.Getenv("AIM_ADMIN_USER"), os.Getenv("AIM_ADMIN_PASSWORD"))
		if err != nil {
			log.Fatal(err)
		}
		if created {
			log.Printf("initial administrator account created for %s", os.Getenv("AIM_ADMIN_USER"))
		}
	}
	maxUpload, err := strconv.ParseInt(env("AIM_MAX_UPLOAD_BYTES", strconv.FormatInt(2<<30, 10)), 10, 64)
	if err != nil || maxUpload < 1 {
		log.Fatal("AIM_MAX_UPLOAD_BYTES must be a positive integer")
	}
	for _, path := range []string{filepath.Join(dataDir, "uploads"), filepath.Join(dataDir, "media")} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			log.Fatal(err)
		}
	}
	var oidcConfig *console.OIDCConfig
	if issuer := strings.TrimSpace(os.Getenv("AIM_OIDC_ISSUER")); issuer != "" {
		oidcConfig = &console.OIDCConfig{
			Issuer: issuer, ClientID: os.Getenv("AIM_OIDC_CLIENT_ID"), ClientSecret: os.Getenv("AIM_OIDC_CLIENT_SECRET"),
			RedirectURL: os.Getenv("AIM_OIDC_REDIRECT_URL"), NormalRole: env("AIM_OIDC_NORMAL_ROLE", "viewer"),
		}
	}
	server, err := console.NewServer(store, secretBox, console.ServerConfig{
		CookieSecure:     strings.EqualFold(env("AIM_COOKIE_SECURE", "true"), "true"),
		DisableLocalAuth: !localAuth, OIDC: oidcConfig,
		FilePickerOrigin: os.Getenv("AIM_FILE_PICKER_ORIGIN"),
		UploadRoot:       filepath.Join(dataDir, "uploads"), MediaRoot: filepath.Join(dataDir, "media"), MaxUpload: maxUpload,
		DocumentRoot: env("AIM_DOCUMENT_ROOT", "/lzcapp/documents"),
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Backups.StartScheduler(); err != nil {
		log.Printf("backup scheduler disabled: %v", err)
	}
	defer server.Backups.StopScheduler()
	httpServer := &http.Server{
		Addr:              env("AIM_LISTEN", ":8080"),
		Handler:           server.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE task streams may remain open for hours.
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		log.Printf("AIM console listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func loadMasterKey(value, path string) ([]byte, error) {
	if value != "" {
		digest := sha256.Sum256([]byte(value))
		return digest[:], nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(b) == 32 {
		return b, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("master key file must contain 32 raw bytes or a base64-encoded 32-byte key")
	}
	return decoded, nil
}
