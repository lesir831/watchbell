package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/watchbell/watchbell/internal/eventvars"
	"github.com/watchbell/watchbell/internal/model"
)

const (
	defaultGitHubAPIURL     = "https://api.github.com"
	defaultGitHubAPIVersion = "2026-03-10"
	maxGitHubReleaseBytes   = 5 * 1024 * 1024
)

type GitHubReleaseChecker struct {
	client *http.Client
}

type GitHubReleaseConfig struct {
	Repository         string `json:"repository"`
	Token              string `json:"token"`
	APIURL             string `json:"apiUrl"`
	APIVersion         string `json:"apiVersion"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	MaxReleases        int    `json:"maxReleases"`
	IncludePrereleases bool   `json:"includePrereleases"`
	NotifyExisting     bool   `json:"notifyExisting"`
}

type githubReleaseState struct {
	Initialized          bool    `json:"initialized"`
	Source               string  `json:"source,omitempty"`
	ETag                 string  `json:"etag,omitempty"`
	SeenReleaseIDs       []int64 `json:"seenReleaseIds,omitempty"`
	ReleaseSnapshotKnown bool    `json:"releaseSnapshotKnown,omitempty"`
	LatestVersion        string  `json:"latestVersion,omitempty"`
}

type githubRelease struct {
	ID          int64                `json:"id"`
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	HTMLURL     string               `json:"html_url"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	PublishedAt string               `json:"published_at"`
	Author      githubReleaseAuthor  `json:"author"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAuthor struct {
	Login string `json:"login"`
}

type githubReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

type githubReleaseFetchResult struct {
	Releases    []githubRelease
	ETag        string
	NotModified bool
}

func NewGitHubReleaseChecker() *GitHubReleaseChecker {
	return &GitHubReleaseChecker{client: &http.Client{}}
}

func (c *GitHubReleaseChecker) Type() string {
	return model.MonitorTypeGitHubRelease
}

func (c *GitHubReleaseChecker) Plugin() model.MonitorPlugin {
	return model.MonitorPlugin{
		ID: model.MonitorTypeGitHubRelease, Name: "GitHub Releases", Builtin: true,
		Description:            "监控 GitHub 仓库，在发布新 Release 时生成通知。",
		DefaultIntervalSeconds: 300,
		DefaultConfig: map[string]any{
			"repository": "owner/repository", "token": "", "apiUrl": defaultGitHubAPIURL,
			"apiVersion": defaultGitHubAPIVersion, "timeoutSeconds": 15, "maxReleases": 20,
			"includePrereleases": false, "notifyExisting": false,
		},
		ConfigFields: []model.PluginConfigField{
			{Key: "repository", Label: "仓库", Type: "string", Required: true, Description: "格式：owner/repository"},
			{Key: "token", Label: "访问令牌", Type: "secret", Secret: true, Description: "公开仓库可以留空"},
			{Key: "apiUrl", Label: "API URL", Type: "url"},
			{Key: "apiVersion", Label: "API 版本", Type: "string"},
			{Key: "timeoutSeconds", Label: "超时时间（秒）", Type: "number"},
			{Key: "maxReleases", Label: "每次检查的 Release 数量", Type: "number"},
			{Key: "includePrereleases", Label: "包含预发布版本", Type: "boolean"},
			{Key: "notifyExisting", Label: "首次检查通知最新版本", Type: "boolean"},
		},
		Events:            []string{"github.release"},
		TemplateVariables: eventvars.EventVariableKeys(model.MonitorTypeGitHubRelease),
	}
}

func (c *GitHubReleaseChecker) Check(ctx context.Context, monitor model.Monitor) (model.CheckResult, error) {
	cfg, owner, repo, source, err := decodeGitHubReleaseConfig(monitor)
	if err != nil {
		return model.CheckResult{}, err
	}
	state := DecodeState(monitor, githubReleaseState{})
	if state.Source != "" && state.Source != source {
		state = githubReleaseState{}
	}
	// Older persisted states do not contain LatestVersion. Force one
	// unconditional refresh for those states so a 304 can never leave the
	// monitor displaying the old "not modified" message without a version.
	etag := state.ETag
	if !state.ReleaseSnapshotKnown {
		etag = ""
	}
	fetched, err := c.fetch(ctx, monitor, cfg, owner, repo, etag)
	if err != nil {
		return model.CheckResult{}, err
	}
	if fetched.NotModified {
		return model.CheckResult{Status: "ok", Message: githubReleaseMessage(state), State: stateToMap(state)}, nil
	}

	seen := make(map[int64]struct{}, len(state.SeenReleaseIDs))
	for _, id := range state.SeenReleaseIDs {
		seen[id] = struct{}{}
	}
	newReleases := make([]githubRelease, 0)
	if state.Initialized {
		for _, release := range fetched.Releases {
			if _, ok := seen[release.ID]; !ok {
				newReleases = append(newReleases, release)
			}
		}
	} else if cfg.NotifyExisting && len(fetched.Releases) > 0 {
		newReleases = append(newReleases, fetched.Releases[0])
	}
	sort.Slice(newReleases, func(i, j int) bool {
		return newReleases[i].PublishedAt < newReleases[j].PublishedAt
	})

	events := make([]model.EventData, 0, len(newReleases))
	for _, release := range newReleases {
		events = append(events, githubReleaseEvent(owner, repo, release))
	}
	state.Initialized = true
	state.Source = source
	state.ETag = fetched.ETag
	state.ReleaseSnapshotKnown = true
	state.LatestVersion = latestGitHubReleaseVersion(fetched.Releases)
	state.SeenReleaseIDs = make([]int64, 0, len(fetched.Releases))
	for _, release := range fetched.Releases {
		state.SeenReleaseIDs = append(state.SeenReleaseIDs, release.ID)
	}

	return model.CheckResult{Status: "ok", Message: githubReleaseMessage(state), State: stateToMap(state), Events: events}, nil
}

func latestGitHubReleaseVersion(releases []githubRelease) string {
	if len(releases) == 0 {
		return ""
	}
	if version := strings.TrimSpace(releases[0].TagName); version != "" {
		return version
	}
	return strings.TrimSpace(releases[0].Name)
}

func githubReleaseMessage(state githubReleaseState) string {
	if version := strings.TrimSpace(state.LatestVersion); version != "" {
		return "最新版本：" + version
	}
	if state.ReleaseSnapshotKnown {
		return "暂无已发布版本"
	}
	return "暂无版本信息"
}

// Inspect bypasses the stored ETag and returns the first release that survives
// the monitor's draft/prerelease filters, regardless of whether it was seen.
func (c *GitHubReleaseChecker) Inspect(ctx context.Context, monitor model.Monitor) (model.Observation, error) {
	cfg, owner, repo, _, err := decodeGitHubReleaseConfig(monitor)
	if err != nil {
		return model.Observation{}, err
	}
	fetched, err := c.fetch(ctx, monitor, cfg, owner, repo, "")
	if err != nil {
		return model.Observation{}, err
	}
	if fetched.NotModified {
		return model.Observation{}, fmt.Errorf("github releases source returned 304 without a conditional request")
	}
	if len(fetched.Releases) == 0 {
		return model.Observation{
			Type: "github.release", Message: "no published releases found", Available: false,
			Payload: map[string]any{"github": map[string]any{
				"owner": owner, "repo": repo, "repository": owner + "/" + repo,
			}},
		}, nil
	}
	event := githubReleaseEvent(owner, repo, fetched.Releases[0])
	return model.Observation{
		Type: event.Type, Fingerprint: event.Fingerprint, Message: "latest published release",
		Available: true, Payload: event.Payload,
	}, nil
}

func decodeGitHubReleaseConfig(monitor model.Monitor) (GitHubReleaseConfig, string, string, string, error) {
	cfg, err := DecodeConfig(monitor, GitHubReleaseConfig{
		APIURL: defaultGitHubAPIURL, APIVersion: defaultGitHubAPIVersion,
		TimeoutSeconds: 15, MaxReleases: 20,
	})
	if err != nil {
		return GitHubReleaseConfig{}, "", "", "", err
	}
	owner, repo, err := parseGitHubRepository(cfg.Repository)
	if err != nil {
		return GitHubReleaseConfig{}, "", "", "", err
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.MaxReleases <= 0 {
		cfg.MaxReleases = 20
	}
	if cfg.MaxReleases > 100 {
		cfg.MaxReleases = 100
	}
	if strings.TrimSpace(cfg.APIURL) == "" {
		cfg.APIURL = defaultGitHubAPIURL
	}
	if strings.TrimSpace(cfg.APIVersion) == "" {
		cfg.APIVersion = defaultGitHubAPIVersion
	}
	if _, err := githubReleasesURL(cfg.APIURL, owner, repo, cfg.MaxReleases); err != nil {
		return GitHubReleaseConfig{}, "", "", "", err
	}
	source := fmt.Sprintf("%s|%s/%s|prerelease=%t", strings.TrimRight(cfg.APIURL, "/"), owner, repo, cfg.IncludePrereleases)
	return cfg, owner, repo, source, nil
}

func (c *GitHubReleaseChecker) fetch(ctx context.Context, monitor model.Monitor, cfg GitHubReleaseConfig, owner, repo, etag string) (githubReleaseFetchResult, error) {
	endpoint, err := githubReleasesURL(cfg.APIURL, owner, repo, cfg.MaxReleases)
	if err != nil {
		return githubReleaseFetchResult{}, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return githubReleaseFetchResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", cfg.APIVersion)
	req.Header.Set("User-Agent", "WatchBell/0.1")
	if token := strings.TrimSpace(cfg.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	client, err := clientForMonitor(c.client, monitor)
	if err != nil {
		return githubReleaseFetchResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return githubReleaseFetchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return githubReleaseFetchResult{NotModified: true}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return githubReleaseFetchResult{}, githubAPIError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubReleaseBytes+1))
	if err != nil {
		return githubReleaseFetchResult{}, err
	}
	if len(body) > maxGitHubReleaseBytes {
		return githubReleaseFetchResult{}, fmt.Errorf("github releases response exceeds %d bytes", maxGitHubReleaseBytes)
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return githubReleaseFetchResult{}, fmt.Errorf("decode github releases: %w", err)
	}
	filtered := make([]githubRelease, 0, len(releases))
	for _, release := range releases {
		if release.Draft || (!cfg.IncludePrereleases && release.Prerelease) {
			continue
		}
		filtered = append(filtered, release)
	}
	return githubReleaseFetchResult{Releases: filtered, ETag: resp.Header.Get("ETag")}, nil
}

func parseGitHubRepository(repository string) (string, string, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(repository), "/"), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("github repository must use owner/repository format")
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSuffix(strings.TrimSpace(parts[1]), ".git")
	if repo == "" {
		return "", "", fmt.Errorf("github repository must use owner/repository format")
	}
	return owner, repo, nil
}

func githubReleasesURL(apiURL, owner, repo string, maxReleases int) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(apiURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("invalid github api url")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases"
	query := base.Query()
	query.Set("per_page", strconv.Itoa(maxReleases))
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func githubAPIError(resp *http.Response) error {
	message := fmt.Sprintf("github releases fetch failed: http %d", resp.StatusCode)
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining == "0" {
		message += "; rate limit exhausted"
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			message += " until " + reset
		}
	}
	return fmt.Errorf("%s", message)
}

func githubReleaseEvent(owner, repo string, release githubRelease) model.EventData {
	assets := make([]map[string]any, 0, len(release.Assets))
	for _, asset := range release.Assets {
		assets = append(assets, map[string]any{"name": asset.Name, "url": asset.DownloadURL, "size": asset.Size})
	}
	return model.EventData{
		Type: "github.release", Fingerprint: fmt.Sprintf("github:release:%d", release.ID),
		Payload: map[string]any{
			"github": map[string]any{
				"owner": owner, "repo": repo, "repository": owner + "/" + repo,
				"release": map[string]any{
					"id": release.ID, "tagName": release.TagName, "name": release.Name,
					"body": release.Body, "url": release.HTMLURL, "prerelease": release.Prerelease,
					"publishedAt": release.PublishedAt, "author": release.Author.Login,
					"assetCount": len(assets), "assets": assets,
				},
			},
		},
	}
}
