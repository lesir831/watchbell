package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/watchbell/watchbell/internal/auth"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/store"
)

func (s *Server) settingsOverview(w http.ResponseWriter, r *http.Request) {
	authEnabled := s.auth != nil && s.auth.Enabled()
	username := ""
	if authEnabled {
		username = s.auth.Username()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authEnabled": authEnabled,
		"username":    username,
	})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil || !s.auth.Enabled() {
		writeError(w, r, validationProblem("当前实例未启用身份认证。", map[string]string{"currentPassword": "启用身份认证后才能在网页中修改密码。"}))
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		ConfirmPassword string `json:"confirmPassword"`
	}
	if !decode(w, r, &input) {
		return
	}
	fields := map[string]string{}
	if input.CurrentPassword == "" {
		fields["currentPassword"] = "请输入当前密码。"
	}
	if errors.Is(auth.ValidatePassword(input.NewPassword), auth.ErrPasswordTooShort) {
		fields["newPassword"] = "新密码至少需要 8 个字符。"
	} else if errors.Is(auth.ValidatePassword(input.NewPassword), auth.ErrPasswordTooLong) {
		fields["newPassword"] = "新密码过长。"
	}
	if input.ConfirmPassword != input.NewPassword {
		fields["confirmPassword"] = "两次输入的新密码不一致。"
	}
	if len(fields) > 0 {
		writeError(w, r, validationProblem("请修正密码设置。", fields))
		return
	}
	credentialVersion, err := s.auth.ChangePassword(r, input.CurrentPassword, input.NewPassword, func(passwordHash string) error {
		return s.store.SetAuthPasswordHashAudited(r.Context(), passwordHash, s.actor(r))
	})
	if retryAfter, limited := auth.LoginRetryAfter(err); limited {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":             "密码验证失败次数过多，请稍后再试。",
			"retryAfterSeconds": seconds,
		})
		return
	}
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, r, validationProblem("当前密码不正确。", map[string]string{"currentPassword": "当前密码不正确。"}))
		return
	}
	if errors.Is(err, auth.ErrPasswordUnchanged) {
		writeError(w, r, validationProblem("新密码不能与当前密码相同。", map[string]string{"newPassword": "请设置一个不同的新密码。"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.RefreshSession(w, r, credentialVersion); errors.Is(err, auth.ErrCredentialChanged) {
		writeError(w, r, &problemError{
			Status: http.StatusConflict, Code: "credential_changed",
			Message: "密码在操作期间被其他方式再次更新，请使用最新密码重新登录。",
		})
		return
	} else if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "password_updated"})
}

func (s *Server) listProxyProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListProxyProfiles(r.Context())
	if err == nil {
		items = sanitizeProxyProfiles(items)
	}
	respond(w, r, items, err)
}

func (s *Server) createProxyProfile(w http.ResponseWriter, r *http.Request) {
	var input model.ProxyProfileInput
	if !decode(w, r, &input) {
		return
	}
	normalizeProxyProfileInput(&input)
	if err := validateProxyProfileInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateProxyNaturalKey(r.Context(), input, 0); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.CreateProxyProfile(r.Context(), input)
	if err == nil {
		id := item.ID
		s.recordAudit(r.Context(), s.actor(r), "create", "proxy", &id, "创建代理 · "+item.Name, sanitizeProxyProfile(item))
		item = sanitizeProxyProfile(item)
	}
	respondCreated(w, r, item, err)
}

func (s *Server) updateProxyProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input model.ProxyProfileInput
	if !decode(w, r, &input) {
		return
	}
	existing, err := s.store.GetProxyProfile(r.Context(), id)
	if err != nil {
		respond(w, r, model.ProxyProfile{}, err)
		return
	}
	normalizeProxyProfileInput(&input)
	if input.ClearPassword {
		input.Password = ""
	} else if input.Password == "" {
		input.Password = existing.Password
	}
	if err := validateProxyProfileInput(input); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.validateProxyNaturalKey(r.Context(), input, id); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.store.UpdateProxyProfile(r.Context(), id, input)
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "update", "proxy", &id, "修改代理 · "+item.Name, map[string]any{
			"before": sanitizeProxyProfile(existing), "after": sanitizeProxyProfile(item),
		})
		item = sanitizeProxyProfile(item)
	}
	respond(w, r, item, err)
}

func (s *Server) deleteProxyProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, _ := s.store.GetProxyProfile(r.Context(), id)
	err := s.store.DeleteProxyProfile(r.Context(), id)
	if errors.Is(err, store.ErrProxyInUse) {
		writeError(w, r, &problemError{
			Status: http.StatusConflict, Code: "proxy_in_use", Message: "这个代理仍被监控使用，请先在相关监控中取消或更换代理。",
			Fields: map[string]string{"proxy": "代理仍被监控引用。"},
		})
		return
	}
	if err == nil {
		s.recordAudit(r.Context(), s.actor(r), "delete", "proxy", &id, "归档代理 · "+item.Name, map[string]any{"retainedAudit": true})
	}
	respondNoContent(w, r, err)
}

func normalizeProxyProfileInput(input *model.ProxyProfileInput) {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	if strings.HasPrefix(input.Host, "[") && strings.HasSuffix(input.Host, "]") {
		input.Host = strings.TrimSuffix(strings.TrimPrefix(input.Host, "["), "]")
	}
}

func validateProxyProfileInput(input model.ProxyProfileInput) error {
	fields := map[string]string{}
	if input.Name == "" {
		fields["name"] = "请输入代理名称。"
	} else if utf8.RuneCountInString(input.Name) > 100 {
		fields["name"] = "代理名称最多 100 个字符。"
	}
	if input.Type != model.ProxyTypeHTTP && input.Type != model.ProxyTypeHTTPS && input.Type != model.ProxyTypeSOCKS5 {
		fields["type"] = "代理类型必须是 HTTP、HTTPS 或 SOCKS5。"
	}
	if input.Host == "" {
		fields["host"] = "请输入代理主机。"
	} else if len(input.Host) > 253 || strings.ContainsAny(input.Host, " \t\r\n/?#@") || strings.Contains(input.Host, "://") || (net.ParseIP(input.Host) == nil && strings.Contains(input.Host, ":")) {
		fields["host"] = "请输入不含协议、路径和端口的主机名或 IP 地址。"
	}
	if input.Port < 1 || input.Port > 65535 {
		fields["port"] = "端口必须在 1 到 65535 之间。"
	}
	if len(input.Username) > 512 {
		fields["username"] = "用户名过长。"
	}
	if len(input.Password) > 2048 {
		fields["password"] = "密码过长。"
	}
	if input.Password != "" && input.Username == "" {
		fields["username"] = "配置代理密码时必须同时填写用户名。"
	}
	if len(fields) > 0 {
		return validationProblem("请修正代理配置。", fields)
	}
	return nil
}

func (s *Server) validateProxyNaturalKey(ctx context.Context, input model.ProxyProfileInput, excludeID int64) error {
	items, err := s.store.ListProxyProfiles(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != excludeID && strings.TrimSpace(item.Name) == input.Name {
			return validationProblem("代理名称不能重复。", map[string]string{"name": "已存在同名代理。"})
		}
	}
	return nil
}

func sanitizeProxyProfile(item model.ProxyProfile) model.ProxyProfile {
	if item.Password != "" {
		item.ConfiguredSecrets = []string{"password"}
	}
	item.Password = ""
	return item
}

func sanitizeProxyProfiles(items []model.ProxyProfile) []model.ProxyProfile {
	result := make([]model.ProxyProfile, 0, len(items))
	for _, item := range items {
		result = append(result, sanitizeProxyProfile(item))
	}
	return result
}
