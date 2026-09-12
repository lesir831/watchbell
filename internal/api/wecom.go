package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/watchbell/watchbell/internal/model"
	"github.com/watchbell/watchbell/internal/notifier"
)

const weComHelp = "WatchBell 指令\n/help 或 帮助\n/monitors [页码] 或 监控 [页码]\n/status [ID] 或 状态 [ID]\n/check ID 或 检查 ID\n/enable ID 或 启用 ID\n/disable ID 或 停用 ID\n先查看监控列表获取 ID；检查完成后会单独回复。"

type weComIncoming struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgID        string   `xml:"MsgId"`
	AgentID      int64    `xml:"AgentID"`
	Event        string   `xml:"Event"`
	EventKey     string   `xml:"EventKey"`
}

func (s *Server) weComCallback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	channel, err := s.store.GetNotifyChannel(r.Context(), id)
	if err != nil || channel.Type != model.ChannelTypeWeCom || !channel.Enabled {
		http.NotFound(w, r)
		return
	}
	cfg, err := notifier.DecodeWeComConfig(channel.Config)
	if err != nil || !cfg.CommandsEnabled {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	timestamp, nonce, signature := q.Get("timestamp"), q.Get("nonce"), q.Get("msg_signature")
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	now := time.Now().Unix()
	if err != nil || stamp < now-300 || stamp > now+300 || len(nonce) > 256 || len(signature) != 40 {
		http.Error(w, "invalid callback", http.StatusForbidden)
		return
	}
	encrypted := q.Get("echostr")
	if r.Method == http.MethodPost {
		var envelope struct {
			XMLName xml.Name `xml:"xml"`
			Encrypt string   `xml:"Encrypt"`
		}
		data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
		if readErr != nil || xml.Unmarshal(data, &envelope) != nil {
			http.Error(w, "invalid callback body", http.StatusBadRequest)
			return
		}
		encrypted = envelope.Encrypt
	}
	if len(encrypted) > 64*1024 {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	plain, err := notifier.DecryptWeCom(cfg, signature, timestamp, nonce, encrypted)
	if err != nil {
		http.Error(w, "invalid callback", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(plain)
		return
	}
	var message weComIncoming
	if xml.Unmarshal(plain, &message) != nil || message.ToUserName != cfg.CorpID || message.AgentID != cfg.AgentID || message.FromUserName == "" || message.CreateTime < now-300 || message.CreateTime > now+300 {
		http.Error(w, "invalid callback message", http.StatusForbidden)
		return
	}
	allowed := false
	for _, user := range cfg.AllowedUserIDs {
		if user == message.FromUserName {
			allowed = true
			break
		}
	}
	if !allowed {
		s.weComReply(w, cfg, message, "此成员没有 WatchBell 指令权限。")
		return
	}
	text := message.Content
	if message.MsgType == "event" && strings.EqualFold(message.Event, "click") {
		text = message.EventKey
	} else if message.MsgType != "text" {
		_, _ = io.WriteString(w, "success")
		return
	}
	// MsgId handles retries whose signature/envelope changes. Click events have
	// no MsgId, so use their authenticated sender, timestamp and event key.
	key := "message:" + message.MsgID
	if message.MsgID == "" {
		key = fmt.Sprintf("event:%s:%d:%s:%s:%s", message.FromUserName, message.CreateTime, message.MsgType, message.Event, text)
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", cfg.CorpID, cfg.AgentID, key)))
	// Bound concurrent commands including detached manual checks before claiming.
	select {
	case s.weComSlots <- struct{}{}:
	default:
		http.Error(w, "command capacity reached; retry later", http.StatusServiceUnavailable)
		return
	}
	detached := false
	defer func() {
		if !detached {
			<-s.weComSlots
		}
	}()
	claimed, err := s.store.ClaimWeComCallback(r.Context(), id, hex.EncodeToString(digest[:]))
	if err != nil {
		http.Error(w, "command storage unavailable", http.StatusServiceUnavailable)
		return
	}
	if !claimed {
		_, _ = io.WriteString(w, "success")
		return
	}
	reply, job := s.executeWeComCommand(r.Context(), channel, cfg, message.FromUserName, text)
	if job != nil {
		detached = true
		go func() { defer func() { <-s.weComSlots }(); job() }()
	}
	s.weComReply(w, cfg, message, reply)
}

func (s *Server) weComReply(w http.ResponseWriter, cfg notifier.WeComConfig, message weComIncoming, content string) {
	// Passive text replies have the same UTF-8 byte limit as application messages.
	if len(content) > 2048 {
		end := 2048 - len("\n…请使用页码查看更多")
		for !utf8.RuneStart(content[end]) {
			end--
		}
		content = content[:end] + "\n…请使用页码查看更多"
	}
	now := time.Now().Unix()
	plain, err := xml.Marshal(struct {
		XMLName    xml.Name `xml:"xml"`
		To         string   `xml:"ToUserName"`
		From       string   `xml:"FromUserName"`
		CreateTime int64    `xml:"CreateTime"`
		MsgType    string   `xml:"MsgType"`
		Content    string   `xml:"Content"`
	}{To: message.FromUserName, From: cfg.CorpID, CreateTime: now, MsgType: "text", Content: content})
	if err != nil {
		http.Error(w, "reply failed", 500)
		return
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		http.Error(w, "reply failed", 500)
		return
	}
	result, err := notifier.EncryptWeCom(cfg, plain, strconv.FormatInt(now, 10), hex.EncodeToString(nonce[:]))
	if err != nil {
		http.Error(w, "reply failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(result)
}

func weComMonitorStatus(m model.Monitor) string {
	enabled := "已停用"
	if m.Enabled {
		enabled = "已启用"
	}
	checked := "尚未检查"
	if m.LastCheckedAt != nil {
		checked = m.LastCheckedAt.In(time.Local).Format("2006-01-02 15:04:05")
	}
	// Never include LastError, LastMessage or config: checker errors can carry URLs/credentials.
	return fmt.Sprintf("#%d %s\n%s · %s\n最近检查：%s\n连续失败：%d", m.ID, weComShortName(m.Name), enabled, m.LastStatus, checked, m.ConsecutiveFailures)
}
func weComShortName(name string) string {
	runes := []rune(strings.Join(strings.Fields(name), " "))
	if len(runes) > 50 {
		return string(runes[:50]) + "…"
	}
	return string(runes)
}

func (s *Server) executeWeComCommand(ctx context.Context, channel model.NotifyChannel, cfg notifier.WeComConfig, user, text string) (string, func()) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || len(fields) > 2 {
		return weComHelp, nil
	}
	command := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	aliases := map[string]string{"帮助": "help", "监控": "monitors", "状态": "status", "检查": "check", "启用": "enable", "停用": "disable"}
	if alias := aliases[command]; alias != "" {
		command = alias
	}
	if command == "help" {
		return weComHelp, nil
	}
	if command == "monitors" || (command == "status" && len(fields) == 1) {
		items, err := s.store.ListMonitors(ctx)
		if err != nil {
			return "读取监控失败，请稍后重试。", nil
		}
		if command == "status" {
			enabled, failures := 0, 0
			for _, m := range items {
				if m.Enabled {
					enabled++
				}
				if m.ConsecutiveFailures > 0 {
					failures++
				}
			}
			return fmt.Sprintf("WatchBell\n监控：%d · 启用：%d · 异常：%d\n正在检查：%d", len(items), enabled, failures, s.scheduler.Health().InFlight), nil
		}
		page := 1
		if len(fields) == 2 {
			page, err = strconv.Atoi(fields[1])
			if err != nil || page < 1 {
				return "用法：/monitors [正整数页码]", nil
			}
		}
		pages := max(1, (len(items)+7)/8)
		if page > pages {
			return fmt.Sprintf("页码超出范围，共 %d 页。", pages), nil
		}
		lines := []string{fmt.Sprintf("监控列表 %d/%d（共 %d 个）", page, pages, len(items))}
		for _, m := range items[(page-1)*8 : min(page*8, len(items))] {
			state := "停用"
			if m.Enabled {
				state = "启用"
			}
			lines = append(lines, fmt.Sprintf("#%d %s [%s]", m.ID, weComShortName(m.Name), state))
		}
		lines = append(lines, "/status ID 查看详情 · /help 查看指令")
		return strings.Join(lines, "\n"), nil
	}
	if command != "status" && command != "check" && command != "enable" && command != "disable" {
		return weComHelp, nil
	}
	if len(fields) != 2 {
		return "请提供监控 ID，例如 /" + command + " 1。", nil
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || id <= 0 {
		return "监控 ID 必须为正整数。", nil
	}
	monitor, err := s.store.GetMonitor(ctx, id)
	if err != nil {
		return "监控不存在或已归档。", nil
	}
	if command == "status" {
		return weComMonitorStatus(monitor), nil
	}
	actor := fmt.Sprintf("wecom:%d:%s", channel.ID, user)
	if command == "enable" || command == "disable" {
		enabled := command == "enable"
		if err := s.store.SetMonitorEnabledAudited(ctx, id, enabled, actor); err != nil {
			return "修改失败，请稍后重新发送指令。", nil
		}
		if enabled {
			return fmt.Sprintf("已启用 #%d %s。", id, weComShortName(monitor.Name)), nil
		}
		return fmt.Sprintf("已停用 #%d %s；已经开始的检查仍会完成。", id, weComShortName(monitor.Name)), nil
	}
	if err := s.store.CreateAuditLog(ctx, actor, "wecom_check_accepted", "monitor", &id, "企业微信受理立即检查", nil); err != nil {
		return "检查未受理：无法记录操作。", nil
	}
	return fmt.Sprintf("已受理 #%d %s 的立即检查，完成后单独回复。", id, weComShortName(monitor.Name)), func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// Re-read authorization before a detached task and before sending its result.
		current, currentCfg, ok := s.weComCommandChannel(jobCtx, channel.ID, user)
		if !ok || currentCfg.CorpID != cfg.CorpID || currentCfg.AgentID != cfg.AgentID {
			return
		}
		err := s.scheduler.RunOnce(jobCtx, id)
		reply := fmt.Sprintf("#%d 检查完成。", id)
		if err != nil {
			reply = fmt.Sprintf("#%d 检查未成功或已在运行，请到 WatchBell 检查记录查看详情。", id)
		} else if m, e := s.store.GetMonitor(jobCtx, id); e == nil {
			reply += "\n" + weComMonitorStatus(m)
		}
		// Use a separate deadline so timeouts can still be reported to the member.
		replyCtx, replyCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer replyCancel()
		s.recordAudit(replyCtx, actor, "wecom_check_finished", "monitor", &id, reply, map[string]any{"success": err == nil})
		current, currentCfg, ok = s.weComCommandChannel(replyCtx, current.ID, user)
		if !ok || currentCfg.CorpID != cfg.CorpID || currentCfg.AgentID != cfg.AgentID {
			return
		}
		if sendErr := s.weCom.SendText(replyCtx, currentCfg, user, reply); sendErr != nil {
			s.recordAudit(replyCtx, actor, "wecom_reply_failed", "channel", &current.ID, "企业微信检查结果回复失败", map[string]any{"error": sendErr.Error()})
		}
	}
}

func (s *Server) weComCommandChannel(ctx context.Context, id int64, user string) (model.NotifyChannel, notifier.WeComConfig, bool) {
	channel, err := s.store.GetNotifyChannel(ctx, id)
	if err != nil || !channel.Enabled || channel.Type != model.ChannelTypeWeCom {
		return channel, notifier.WeComConfig{}, false
	}
	cfg, err := notifier.DecodeWeComConfig(channel.Config)
	if err == nil && cfg.CommandsEnabled {
		for _, member := range cfg.AllowedUserIDs {
			if member == user {
				return channel, cfg, true
			}
		}
	}
	return channel, cfg, false
}

func (s *Server) syncWeComMenu(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	channel, err := s.store.GetNotifyChannel(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cfg, err := notifier.DecodeWeComConfig(channel.Config)
	if channel.Type != model.ChannelTypeWeCom || !channel.Enabled || err != nil || !cfg.CommandsEnabled {
		writeError(w, r, validationProblem("请先启用企业微信渠道及指令功能。", nil))
		return
	}
	if err = s.weCom.SyncMenu(r.Context(), cfg); err != nil {
		writeError(w, r, err)
		return
	}
	s.recordAudit(r.Context(), s.actor(r), "wecom_menu_sync", "channel", &id, "同步企业微信应用菜单", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
}
