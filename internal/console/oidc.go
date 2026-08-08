package console

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcStateCookie = "aim_oidc_state"
	oidcNonceCookie = "aim_oidc_nonce"
	oidcPKCECookie  = "aim_oidc_pkce"
)

type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	NormalRole   string
}

type OIDCAuth struct {
	auth         *Auth
	config       OIDCConfig
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	provider     *oidc.Provider
}

func NewOIDCAuth(ctx context.Context, auth *Auth, config OIDCConfig) (*OIDCAuth, error) {
	if strings.TrimSpace(config.Issuer) == "" || strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" {
		return nil, errors.New("OIDC issuer, client ID and client secret are required")
	}
	if config.NormalRole == "" {
		config.NormalRole = "viewer"
	}
	if config.NormalRole != "viewer" && config.NormalRole != "operator" {
		return nil, errors.New("AIM_OIDC_NORMAL_ROLE must be viewer or operator")
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, strings.TrimRight(config.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	return &OIDCAuth{
		auth:   auth,
		config: config,
		oauth2Config: oauth2.Config{
			ClientID: config.ClientID, ClientSecret: config.ClientSecret,
			Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: config.ClientID}),
		provider: provider,
	}, nil
}

func (o *OIDCAuth) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法启动 OIDC 登录")
		return
	}
	nonce, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法启动 OIDC 登录")
		return
	}
	pkceVerifier := oauth2.GenerateVerifier()
	o.setTransientCookie(w, oidcStateCookie, state, 600)
	o.setTransientCookie(w, oidcNonceCookie, nonce, 600)
	o.setTransientCookie(w, oidcPKCECookie, pkceVerifier, 600)
	config := o.oauth2Config
	config.RedirectURL = o.redirectURL(r)
	http.Redirect(w, r, config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(pkceVerifier)), http.StatusFound)
}

func (o *OIDCAuth) Callback(w http.ResponseWriter, r *http.Request) {
	stateCookie, stateErr := r.Cookie(oidcStateCookie)
	nonceCookie, nonceErr := r.Cookie(oidcNonceCookie)
	pkceCookie, pkceErr := r.Cookie(oidcPKCECookie)
	o.clearTransientCookies(w)
	state := r.URL.Query().Get("state")
	if stateErr != nil || nonceErr != nil || pkceErr != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(stateCookie.Value)) != 1 {
		writeError(w, http.StatusBadRequest, "OIDC state 校验失败，请重新登录")
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		writeError(w, http.StatusUnauthorized, "OIDC 登录被取消或拒绝")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "OIDC 回调缺少授权码")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	config := o.oauth2Config
	config.RedirectURL = o.redirectURL(r)
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(pkceCookie.Value))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "OIDC 授权码交换失败")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		writeError(w, http.StatusUnauthorized, "OIDC 响应缺少 ID Token")
		return
	}
	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "OIDC ID Token 校验失败")
		return
	}
	var claims struct {
		Subject           string   `json:"sub"`
		Nonce             string   `json:"nonce"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Subject == "" || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(nonceCookie.Value)) != 1 {
		writeError(w, http.StatusUnauthorized, "OIDC claims 或 nonce 校验失败")
		return
	}
	if len(claims.Groups) == 0 {
		userInfo, err := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
		if err != nil || userInfo.Claims(&claims) != nil {
			writeError(w, http.StatusUnauthorized, "OIDC 用户信息读取失败")
			return
		}
	}
	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = claims.Name
	}
	role := o.config.NormalRole
	for _, group := range claims.Groups {
		if strings.EqualFold(group, "ADMIN") {
			role = "admin"
			break
		}
	}
	user, err := o.auth.Store.UpsertOIDCUser(r.Context(), claims.Subject, username, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建 OIDC 用户")
		return
	}
	if _, err := o.auth.CreateSession(w, r, user); err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建会话")
		return
	}
	o.auth.Store.Audit(r.Context(), user, remoteIP(r), "oidc_login", "session", "", `{}`)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (o *OIDCAuth) redirectURL(r *http.Request) string {
	if o.config.RedirectURL != "" {
		return o.config.RedirectURL
	}
	host := r.Host
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	scheme := "https"
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/api/v1/oidc/callback"}).String()
}

func (o *OIDCAuth) setTransientCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/api/v1/oidc/", HttpOnly: true, Secure: o.auth.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (o *OIDCAuth) clearTransientCookies(w http.ResponseWriter) {
	o.setTransientCookie(w, oidcStateCookie, "", -1)
	o.setTransientCookie(w, oidcNonceCookie, "", -1)
	o.setTransientCookie(w, oidcPKCECookie, "", -1)
}
