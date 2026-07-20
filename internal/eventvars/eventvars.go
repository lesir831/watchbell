package eventvars

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/datetime"
	"github.com/watchbell/watchbell/internal/model"
)

// Definition documents a value that can be referenced by rule conditions,
// notification templates, and notification-channel templates.
type Definition struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	ValueType   string   `json:"valueType"`
	AvailableIn []string `json:"availableIn"`
}

type Module struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Variables []Definition `json:"variables"`
}

type Catalog struct {
	System  []Definition `json:"system"`
	Globals []Definition `json:"globals"`
	Modules []Module     `json:"modules"`
}

var systemDefinitions = []Definition{
	{Key: "monitor.id", Label: "监控 ID", Description: "产生事件的监控 ID。", ValueType: "number"},
	{Key: "monitor.name", Label: "监控名称", Description: "产生事件的监控名称。", ValueType: "string"},
	{Key: "monitor.type", Label: "监控类型", Description: "监控模块标识，例如 rss 或 github_release。", ValueType: "string"},
	{Key: "rule.id", Label: "规则 ID", Description: "发送通知时命中的规则 ID；查看事件快照时不可用。", ValueType: "number"},
	{Key: "rule.name", Label: "规则名称", Description: "发送通知时命中的规则名称；查看事件快照时不可用。", ValueType: "string"},
	{Key: "rule.matched", Label: "命中值", Description: "规则判断中命中的条件值列表。", ValueType: "array"},
	{Key: "event.id", Label: "事件 ID", Description: "WatchBell 内部持久化事件 ID；实时变量检查不会创建事件，因此不可用。", ValueType: "number"},
	{Key: "event.type", Label: "事件类型", Description: "事件或当前观测对应的类型，例如 rss.item。", ValueType: "string"},
	{Key: "event.fingerprint", Label: "事件指纹", Description: "持久化事件中用于去重；实时检查显示当前观测的只读标识，不代表已经创建事件。", ValueType: "string"},
	{Key: "event.time", Label: "事件时间", Description: "WatchBell 创建持久化事件的时间；通知模板按系统设置的时区和格式渲染，实时变量检查中不可用。", ValueType: "datetime"},
	{Key: "message.subject", Label: "通知标题", Description: "渲染完成后的通知标题，仅用于 Webhook 等渠道动态配置。", ValueType: "string"},
	{Key: "message.body", Label: "通知正文", Description: "渲染完成后的通知正文，仅用于 Webhook 等渠道动态配置。", ValueType: "string"},
}

// globalDefinitions are deliberately short aliases. They provide one template
// across every monitor type, which is particularly useful for Bark's click URL.
var globalDefinitions = []Definition{
	{Key: "url", Label: "跳转地址", Description: "当前事件最适合打开的页面地址；可显式用于 Bark 点击跳转，私有或带访问凭据的地址请谨慎外发。", ValueType: "url"},
	{Key: "title", Label: "标题", Description: "当前事件的人类可读标题。", ValueType: "string"},
	{Key: "summary", Label: "摘要", Description: "当前事件的简短说明；模块没有独立摘要时可能与正文相同。", ValueType: "string"},
	{Key: "content", Label: "正文", Description: "当前事件的主要正文内容。", ValueType: "string"},
	{Key: "author", Label: "作者", Description: "源内容作者；源模块不提供作者时为空。", ValueType: "string"},
	{Key: "publishedAt", Label: "发布时间", Description: "源内容的 RFC3339 发布时间；源模块不提供时为空。", ValueType: "datetime"},
	{Key: "status", Label: "状态", Description: "模块归一化后的事件状态，例如 available、changed 或 released。", ValueType: "string"},
}

var moduleDefinitions = map[string][]Definition{
	model.MonitorTypeRSS: {
		{Key: "rss.title", Label: "条目标题", Description: "RSS、Atom 或 JSON Feed 条目的标题。", ValueType: "string"},
		{Key: "rss.link", Label: "条目链接", Description: "条目原文链接。", ValueType: "url"},
		{Key: "rss.author", Label: "条目作者", Description: "Feed 提供的条目作者名称。", ValueType: "string"},
		{Key: "rss.summary", Label: "条目摘要", Description: "Feed 提供的摘要或 description。", ValueType: "string"},
		{Key: "rss.content", Label: "条目正文", Description: "条目正文；监控开启“包含完整正文”时优先使用完整 content。", ValueType: "string"},
		{Key: "rss.publishedAt", Label: "条目发布时间", Description: "Feed 发布时间；可解析时规范化为 RFC3339，无法解析时保留源值并不能用于“最近时间内”判断。", ValueType: "datetime"},
		{Key: "rss.sourceTitle", Label: "订阅源标题", Description: "Feed 自身的标题。", ValueType: "string"},
		{Key: "rss.sourceLink", Label: "订阅源链接", Description: "Feed 声明的网站链接。", ValueType: "url"},
	},
	model.MonitorTypeTestFlight: {
		{Key: "testflight.url", Label: "邀请地址", Description: "公开 TestFlight 邀请页面地址。", ValueType: "url"},
		{Key: "testflight.status", Label: "名额状态", Description: "识别到的当前状态，例如 available、full 或 unknown。", ValueType: "string"},
		{Key: "testflight.message", Label: "状态说明", Description: "对当前 TestFlight 状态的文字说明。", ValueType: "string"},
	},
	model.MonitorTypeWebpage: {
		{Key: "webpage.url", Label: "网页地址", Description: "被监控网页的地址。", ValueType: "url"},
		{Key: "webpage.selector", Label: "CSS 选择器", Description: "用于截取网页内容的选择器；未配置时为空。", ValueType: "string"},
		{Key: "webpage.oldHash", Label: "旧内容哈希", Description: "变化前规范化内容的 SHA-256。", ValueType: "string"},
		{Key: "webpage.newHash", Label: "新内容哈希", Description: "变化后规范化内容的 SHA-256。", ValueType: "string"},
		{Key: "webpage.summary", Label: "新内容摘要", Description: "变化后网页文本的截断摘要。", ValueType: "string"},
	},
	model.MonitorTypeGitHubRelease: {
		{Key: "github.owner", Label: "仓库所有者", Description: "GitHub 仓库 owner。", ValueType: "string"},
		{Key: "github.repo", Label: "仓库名", Description: "GitHub 仓库名称。", ValueType: "string"},
		{Key: "github.repository", Label: "完整仓库名", Description: "owner/repository 格式的仓库名。", ValueType: "string"},
		{Key: "github.release.id", Label: "Release ID", Description: "GitHub Release 数字 ID。", ValueType: "number"},
		{Key: "github.release.tagName", Label: "标签", Description: "Release 对应的 Git tag。", ValueType: "string"},
		{Key: "github.release.name", Label: "Release 名称", Description: "Release 标题。", ValueType: "string"},
		{Key: "github.release.body", Label: "Release 正文", Description: "Release notes 的 Markdown 正文。", ValueType: "string"},
		{Key: "github.release.url", Label: "Release 地址", Description: "GitHub Release 网页地址。", ValueType: "url"},
		{Key: "github.release.prerelease", Label: "是否预发布", Description: "是否为 prerelease。", ValueType: "boolean"},
		{Key: "github.release.publishedAt", Label: "发布时间", Description: "GitHub 提供的 RFC3339 发布时间。", ValueType: "datetime"},
		{Key: "github.release.author", Label: "发布者", Description: "发布 Release 的 GitHub 用户名。", ValueType: "string"},
		{Key: "github.release.assetCount", Label: "附件数量", Description: "Release 附件数量。", ValueType: "number"},
		{Key: "github.release.assets", Label: "附件列表", Description: "附件对象列表，每项包含 name、url 和 size。", ValueType: "array"},
	},
}

var moduleNames = map[string]string{
	model.MonitorTypeRSS:           "RSS / Atom",
	model.MonitorTypeTestFlight:    "TestFlight",
	model.MonitorTypeWebpage:       "网页变化",
	model.MonitorTypeGitHubRelease: "GitHub Releases",
}

func VariableCatalog() Catalog {
	moduleIDs := []string{
		model.MonitorTypeRSS,
		model.MonitorTypeTestFlight,
		model.MonitorTypeWebpage,
		model.MonitorTypeGitHubRelease,
	}
	modules := make([]Module, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		definitions := cloneDefinitions(moduleDefinitions[moduleID])
		setAvailability(definitions, "rule", "template", "channel")
		modules = append(modules, Module{ID: moduleID, Name: moduleNames[moduleID], Variables: definitions})
	}
	system := cloneDefinitions(systemDefinitions)
	for index := range system {
		if strings.HasPrefix(system[index].Key, "message.") {
			system[index].AvailableIn = []string{"channel"}
		} else {
			system[index].AvailableIn = []string{"template", "channel"}
		}
	}
	globals := cloneDefinitions(globalDefinitions)
	setAvailability(globals, "rule", "template", "channel")
	return Catalog{System: system, Globals: globals, Modules: modules}
}

// EventVariableKeys returns fields that can exist in an event payload and are
// therefore safe to expose in the visual rule builder.
func EventVariableKeys(monitorType string) []string {
	result := make([]string, 0, len(globalDefinitions)+len(moduleDefinitions[monitorType]))
	for _, item := range moduleDefinitions[monitorType] {
		result = append(result, item.Key)
	}
	for _, item := range globalDefinitions {
		result = append(result, item.Key)
	}
	return result
}

func KnownKey(monitorType, key string) bool {
	for _, item := range EventVariableKeys(monitorType) {
		if item == key {
			return true
		}
	}
	return false
}

func EventDefinition(monitorType, key string) (Definition, bool) {
	for _, item := range globalDefinitions {
		if item.Key == key {
			return item, true
		}
	}
	for _, item := range moduleDefinitions[monitorType] {
		if item.Key == key {
			return item, true
		}
	}
	return Definition{}, false
}

func DocumentedKey(monitorType, key string) bool {
	for _, item := range systemDefinitions {
		if item.Key == key {
			return true
		}
	}
	return KnownKey(monitorType, key)
}

func SnapshotKeys(monitorType string) []string {
	keys := make([]string, 0, len(systemDefinitions)+len(globalDefinitions)+len(moduleDefinitions[monitorType]))
	for _, item := range systemDefinitions {
		if strings.HasPrefix(item.Key, "monitor.") || strings.HasPrefix(item.Key, "event.") {
			keys = append(keys, item.Key)
		}
	}
	return append(keys, EventVariableKeys(monitorType)...)
}

// EnrichPayload adds the cross-module shortcut variables while retaining the
// original namespaced data. It returns a new top-level map so callers can use it
// safely with immutable event payloads.
func EnrichPayload(monitor model.Monitor, payload map[string]any) map[string]any {
	result := make(map[string]any, len(payload)+len(globalDefinitions))
	for key, value := range payload {
		result[key] = value
	}
	values := normalizedValues(monitor, payload)
	for _, definition := range globalDefinitions {
		result[definition.Key] = values[definition.Key]
	}
	return result
}

func EventData(monitor model.Monitor, event model.Event, payload map[string]any) map[string]any {
	data := map[string]any{}
	for key, value := range EnrichPayload(monitor, payload) {
		data[key] = value
	}
	data["monitor"] = map[string]any{"id": monitor.ID, "name": monitor.Name, "type": monitor.Type}
	data["event"] = map[string]any{
		"id": event.ID, "type": event.Type, "fingerprint": event.Fingerprint,
		"time": event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	return data
}

// EventDataForDisplay keeps persisted timestamps machine-readable while
// rendering event.time for user-facing templates with the configured policy.
func EventDataForDisplay(monitor model.Monitor, event model.Event, payload map[string]any, timezone, format string) (map[string]any, error) {
	data := EventData(monitor, event, payload)
	formatted, err := datetime.Format(event.CreatedAt, timezone, format)
	if err != nil {
		return nil, err
	}
	data["event"].(map[string]any)["time"] = formatted
	return data, nil
}

// ObservationData builds the event-like context used by live variable
// diagnostics. An observation is deliberately not assigned an event ID or
// event time because inspecting a source does not persist an event.
func ObservationData(monitor model.Monitor, observation model.Observation) map[string]any {
	data := map[string]any{}
	for key, value := range EnrichPayload(monitor, observation.Payload) {
		data[key] = value
	}
	if !observation.Available {
		// Source-level context remains useful when a feed/repository is empty,
		// but no concrete item exists from which to derive an event status.
		data["status"] = ""
	}
	data["monitor"] = map[string]any{"id": monitor.ID, "name": monitor.Name, "type": monitor.Type}
	data["event"] = map[string]any{
		"type": observation.Type, "fingerprint": observation.Fingerprint,
	}
	return data
}

func Flatten(data map[string]any) map[string]any {
	result := map[string]any{}
	var visit func(string, any)
	visit = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				visit(path, typed[key])
			}
		default:
			if prefix != "" {
				result[prefix] = value
			}
		}
	}
	visit("", data)
	return result
}

func normalizedValues(monitor model.Monitor, payload map[string]any) map[string]any {
	values := map[string]any{
		"url": "", "title": "", "summary": "", "content": "",
		"author": "", "publishedAt": "", "status": "",
	}
	switch monitor.Type {
	case model.MonitorTypeRSS:
		values["url"] = firstNonEmpty(stringAt(payload, "rss.link"), stringAt(payload, "rss.sourceLink"), safeMonitorConfigURL(monitor, "url"))
		values["title"] = firstNonEmpty(stringAt(payload, "rss.title"), stringAt(payload, "rss.sourceTitle"), monitor.Name)
		values["summary"] = stringAt(payload, "rss.summary")
		values["content"] = stringAt(payload, "rss.content")
		values["author"] = stringAt(payload, "rss.author")
		values["publishedAt"] = stringAt(payload, "rss.publishedAt")
		values["status"] = "published"
	case model.MonitorTypeTestFlight:
		values["url"] = firstNonEmpty(stringAt(payload, "testflight.url"), safeMonitorConfigURL(monitor, "url"))
		values["title"] = monitor.Name
		values["summary"] = stringAt(payload, "testflight.message")
		values["content"] = stringAt(payload, "testflight.message")
		values["status"] = stringAt(payload, "testflight.status")
	case model.MonitorTypeWebpage:
		values["url"] = firstNonEmpty(stringAt(payload, "webpage.url"), safeMonitorConfigURL(monitor, "url"))
		values["title"] = monitor.Name
		values["summary"] = stringAt(payload, "webpage.summary")
		values["content"] = stringAt(payload, "webpage.summary")
		values["status"] = firstNonEmpty(stringAt(payload, "webpage.status"), "changed")
	case model.MonitorTypeGitHubRelease:
		repository := firstNonEmpty(stringAt(payload, "github.repository"), monitorConfigString(monitor, "repository"))
		values["url"] = firstNonEmpty(stringAt(payload, "github.release.url"), githubRepositoryURL(repository))
		values["title"] = firstNonEmpty(stringAt(payload, "github.release.name"), stringAt(payload, "github.release.tagName"), repository, monitor.Name)
		values["summary"] = stringAt(payload, "github.release.body")
		values["content"] = stringAt(payload, "github.release.body")
		values["author"] = stringAt(payload, "github.release.author")
		values["publishedAt"] = stringAt(payload, "github.release.publishedAt")
		if boolAt(payload, "github.release.prerelease") {
			values["status"] = "prerelease"
		} else {
			values["status"] = "released"
		}
	}
	if strings.TrimSpace(fmt.Sprint(values["summary"])) == "" {
		values["summary"] = values["content"]
	}
	if strings.TrimSpace(fmt.Sprint(values["content"])) == "" {
		values["content"] = values["summary"]
	}
	return values
}

func lookup(payload map[string]any, path string) (any, bool) {
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func stringAt(payload map[string]any, path string) string {
	value, ok := lookup(payload, path)
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func boolAt(payload map[string]any, path string) bool {
	value, ok := lookup(payload, path)
	if !ok {
		return false
	}
	result, _ := value.(bool)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func monitorConfigString(monitor model.Monitor, key string) string {
	var config map[string]any
	if len(monitor.Config) == 0 || json.Unmarshal(monitor.Config, &config) != nil {
		return ""
	}
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// safeMonitorConfigURL is used only for an implicit fallback from monitor
// configuration. Event-provided links remain untouched because they are part
// of the persisted event itself. Configuration URLs, on the other hand, may
// contain credentials intended only for fetching a private source and must not
// silently become notification click targets.
func safeMonitorConfigURL(monitor model.Monitor, key string) string {
	raw := strings.TrimSpace(monitorConfigString(monitor, key))
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	for key := range query {
		if sensitiveQueryKey(key) {
			return ""
		}
	}
	return raw
}

func sensitiveQueryKey(key string) bool {
	normalized := strings.Map(func(value rune) rune {
		if value >= 'A' && value <= 'Z' {
			return value + ('a' - 'A')
		}
		if (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') {
			return value
		}
		return -1
	}, key)
	if normalized == "key" || normalized == "sig" || normalized == "auth" || normalized == "jwt" {
		return true
	}
	for _, marker := range []string{
		"token", "secret", "password", "passwd", "credential", "signature",
		"apikey", "accesskey", "privatekey", "authorization", "authentication", "sessionid",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func githubRepositoryURL(repository string) string {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if repository == "" {
		return ""
	}
	return "https://github.com/" + repository
}

func cloneDefinitions(input []Definition) []Definition {
	result := append([]Definition(nil), input...)
	for index := range result {
		result[index].AvailableIn = append([]string(nil), input[index].AvailableIn...)
	}
	return result
}

func setAvailability(definitions []Definition, values ...string) {
	for index := range definitions {
		definitions[index].AvailableIn = append([]string(nil), values...)
	}
}
