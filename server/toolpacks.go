package server

// Managed, opt-in portable tools. All state and executables live below the
// instance data directory; installing a package never executes a scan.
import (
	"archive/zip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

type toolpackComponent struct {
	Version     string `json:"version,omitempty"`
	Path        string `json:"path,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Source      string `json:"source,omitempty"`
	InstalledAt string `json:"installed_at,omitempty"`
}

type toolpackState struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	Enabled          bool              `json:"enabled"`
	Engine           toolpackComponent `json:"engine"`
	Templates        toolpackComponent `json:"templates"`
	Status           string            `json:"status"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	Downloaded       int64             `json:"downloaded"`
	Total            int64             `json:"total"`
	LastCheck        string            `json:"last_check,omitempty"`
	CheckOutput      string            `json:"check_output,omitempty"`
	Running          int               `json:"running"`
	InstallDirectory string            `json:"install_directory"`
}

type toolpackAsset struct {
	Version     string `json:"version"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"-"`
}
type toolpackCatalog struct {
	Engine    toolpackAsset `json:"engine"`
	Templates toolpackAsset `json:"templates"`
	Platform  string        `json:"platform"`
}
type toolpackRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		APIURL string `json:"url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

type toolpackManager struct {
	mu                  sync.Mutex
	root                string
	state               toolpackState
	client              *http.Client
	cancel              context.CancelFunc
	catalog             *toolpackCatalog
	versionProbe        func(context.Context, string) (string, error) // tests can validate install transitions without executing a downloaded fixture
	downloadIdleTimeout time.Duration
}

var toolpackManagers sync.Map
var toolpackVersionRE = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-.][A-Za-z0-9.-]+)?$`)

func newToolpackManager(dataDir string) *toolpackManager {
	root, _ := filepath.Abs(filepath.Join(dataDir, "toolpacks"))
	m := &toolpackManager{root: root, client: &http.Client{Transport: newToolpackHTTPTransport(), Timeout: 60 * time.Second}, downloadIdleTimeout: 90 * time.Second}
	m.state = toolpackState{ID: "nuclei", Name: "Nuclei", Description: "模板检测引擎；按需安装，在当前任务范围内使用明确选定的模板。", Status: "not_installed", InstallDirectory: root}
	if b, err := os.ReadFile(filepath.Join(root, "state.json")); err == nil {
		if json.Unmarshal(b, &m.state) != nil {
			m.state.Status = "failed"
			m.state.Error = "安装状态文件损坏，请重新安装"
		}
	}
	m.state.InstallDirectory = root
	m.state.Running = 0
	for _, c := range []toolpackComponent{m.state.Engine, m.state.Templates} {
		if c.Path != "" && !withinToolpackRoot(root, c.Path) {
			m.state.Enabled = false
			m.state.Status = "failed"
			m.state.Error = "安装状态中的路径不属于本实例，请重新安装"
		}
	}
	if m.state.Status == "installing" {
		m.state.Status = "failed"
		m.state.Error = "上次安装被中断，可重新安装；原有可用版本保留"
	}
	return m
}

func (s *Server) toolpackManager() *toolpackManager {
	if v, ok := toolpackManagers.Load(s); ok {
		return v.(*toolpackManager)
	}
	m := newToolpackManager(s.m.dir)
	v, _ := toolpackManagers.LoadOrStore(s, m)
	return v.(*toolpackManager)
}

func (m *toolpackManager) snapshot() toolpackState { m.mu.Lock(); defer m.mu.Unlock(); return m.state }
func (m *toolpackManager) persistLocked() error {
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(m.root, "state.json.tmp")
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.root, "state.json"))
}

func (s *Server) registerToolpackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/toolpacks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"tools": []toolpackState{s.toolpackManager().snapshot()}})
	})
	mux.HandleFunc("GET /api/toolpacks/nuclei/catalog", func(w http.ResponseWriter, r *http.Request) {
		m := s.toolpackManager()
		if r.URL.Query().Get("refresh") == "1" {
			m.mu.Lock()
			m.catalog = nil
			m.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		c, err := m.discover(ctx)
		if err != nil {
			writeErr(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, c)
	})
	mux.HandleFunc("POST /api/toolpacks/nuclei/action", s.toolpackAction)
}

func (s *Server) toolpackAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action    string `json:"action"`
		Component string `json:"component"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	m := s.toolpackManager()
	var err error
	switch in.Action {
	case "install":
		err = m.install(s.ctx, in.Component)
	case "cancel":
		m.mu.Lock()
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Unlock()
	case "enable", "disable":
		m.mu.Lock()
		if in.Action == "enable" && (m.state.Engine.Path == "" || m.state.Templates.Path == "") {
			err = errors.New("请先完成引擎和模板安装")
		} else {
			m.state.Enabled = in.Action == "enable"
			err = m.persistLocked()
		}
		m.mu.Unlock()
	case "check":
		err = m.check(r.Context())
	case "uninstall":
		err = m.uninstall()
	default:
		writeErr(w, 400, "未知工具操作")
		return
	}
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, m.snapshot())
}

func (m *toolpackManager) get(ctx context.Context, addr string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "prtseek-Personal-Toolpacks/1.0")
	setToolpackDownloadHeaders(req)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载源返回 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("下载内容超过限制")
	}
	return b, err
}

func (m *toolpackManager) release(ctx context.Context, repo string) (toolpackRelease, error) {
	var rel toolpackRelease
	b, err := m.get(ctx, "https://api.github.com/repos/projectdiscovery/"+repo+"/releases/latest", 4<<20)
	if err == nil {
		err = json.Unmarshal(b, &rel)
	}
	if err == nil && !toolpackVersionRE.MatchString(rel.Tag) {
		err = errors.New("发布版本号不合法或没有可用版本")
	}
	return rel, err
}

func officialToolpackURL(addr, repo string) bool {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return false
	}
	return strings.HasPrefix(u.Path, "/projectdiscovery/"+repo+"/releases/download/") || strings.HasPrefix(u.Path, "/projectdiscovery/"+repo+"/archive/refs/tags/")
}

// GitHub's official asset API avoids an unnecessary github.com page redirect.
// Keep the release URL separately for provenance displayed to the user.
func toolpackAssetDownloadURL(browserURL, apiURL, repo string) string {
	u, err := url.Parse(apiURL)
	prefix := "/repos/projectdiscovery/" + repo + "/releases/assets/"
	if err == nil && u.Scheme == "https" && u.Host == "api.github.com" && u.User == nil && strings.HasPrefix(u.Path, prefix) {
		id := strings.TrimPrefix(u.Path, prefix)
		if id != "" && strings.Trim(id, "0123456789") == "" {
			return apiURL
		}
	}
	return browserURL
}

func setToolpackDownloadHeaders(req *http.Request) {
	if req.URL.Host == "api.github.com" && strings.Contains(req.URL.Path, "/releases/assets/") {
		req.Header.Set("Accept", "application/octet-stream")
	}
}

func checksumFor(body []byte, name string) (string, error) {
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			h := strings.ToLower(fields[0])
			b, err := hex.DecodeString(h)
			if err != nil || len(b) != 32 {
				return "", errors.New("校验清单中的 SHA-256 无效")
			}
			return h, nil
		}
	}
	return "", fmt.Errorf("官方校验清单未包含 %s", name)
}

func (m *toolpackManager) discover(ctx context.Context) (toolpackCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	m.mu.Lock()
	cached := m.catalog
	m.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	c := toolpackCatalog{Platform: runtime.GOOS + "/" + runtime.GOARCH}
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macOS"
	}
	rel, err := m.release(ctx, "nuclei")
	if err != nil {
		return c, fmt.Errorf("获取 Nuclei 版本失败: %w", err)
	}
	name := "nuclei_" + strings.TrimPrefix(rel.Tag, "v") + "_" + osName + "_" + runtime.GOARCH + ".zip"
	checksumURL := ""
	checksumDownloadURL := ""
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, "_checksums.txt") {
			checksumURL = a.URL
			checksumDownloadURL = toolpackAssetDownloadURL(a.URL, a.APIURL, "nuclei")
		}
		if a.Name == name {
			c.Engine = toolpackAsset{Version: rel.Tag, Name: name, URL: a.URL, DownloadURL: toolpackAssetDownloadURL(a.URL, a.APIURL, "nuclei"), Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")}
		}
	}
	if c.Engine.URL == "" {
		return c, fmt.Errorf("Nuclei 官方未提供 %s 原生包", c.Platform)
	}
	if !officialToolpackURL(c.Engine.URL, "nuclei") || !officialToolpackURL(checksumURL, "nuclei") {
		return c, errors.New("发布包来源不是官方地址")
	}
	b, err := m.get(ctx, checksumDownloadURL, 1<<20)
	if err != nil {
		return c, err
	}
	verified, err := checksumFor(b, name)
	if err != nil {
		return c, err
	}
	if c.Engine.SHA256 != "" && c.Engine.SHA256 != verified {
		return c, errors.New("发布元数据与校验清单不一致")
	}
	c.Engine.SHA256 = verified
	tpl, err := m.release(ctx, "nuclei-templates")
	if err != nil {
		return c, fmt.Errorf("获取模板版本失败: %w", err)
	}
	tplName := "nuclei-templates-" + strings.TrimPrefix(tpl.Tag, "v") + ".zip"
	checksumURL = ""
	checksumDownloadURL = ""
	for _, a := range tpl.Assets {
		if strings.HasSuffix(a.Name, "_checksums.txt") {
			checksumURL = a.URL
			checksumDownloadURL = toolpackAssetDownloadURL(a.URL, a.APIURL, "nuclei-templates")
		}
	}
	if !officialToolpackURL(checksumURL, "nuclei-templates") {
		return c, errors.New("模板缺少官方校验清单")
	}
	b, err = m.get(ctx, checksumDownloadURL, 1<<20)
	if err != nil {
		return c, err
	}
	hash, err := checksumFor(b, tplName)
	if err != nil {
		return c, err
	}
	c.Templates = toolpackAsset{Version: tpl.Tag, Name: tplName, URL: "https://github.com/projectdiscovery/nuclei-templates/archive/refs/tags/" + tpl.Tag + ".zip", DownloadURL: "https://codeload.github.com/projectdiscovery/nuclei-templates/zip/refs/tags/" + tpl.Tag, SHA256: hash}
	m.mu.Lock()
	m.catalog = &c
	m.mu.Unlock()
	return c, nil
}

func (m *toolpackManager) install(parent context.Context, component string) error {
	if component == "" {
		component = "all"
	}
	if component != "all" && component != "engine" && component != "templates" {
		return errors.New("component 需为 all、engine 或 templates")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return errors.New("安装正在进行，请等待或取消")
	}
	wasComplete := m.state.Engine.Path != "" && m.state.Templates.Path != ""
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.state.Status = "installing"
	m.state.Stage = "获取官方版本和校验信息"
	m.state.Error = ""
	m.state.Downloaded = 0
	m.state.Total = 0
	if err := m.persistLocked(); err != nil {
		cancel()
		m.cancel = nil
		return err
	}
	go func() {
		c, err := m.discover(ctx)
		if err == nil && (component == "all" || component == "engine") && !m.reusableEngine(ctx, c.Engine) {
			err = m.installComponent(ctx, "engine", c.Engine)
		}
		if err == nil && (component == "all" || component == "templates") {
			err = m.installComponent(ctx, "templates", c.Templates)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		cancel()
		m.cancel = nil
		m.state.Stage = ""
		if err != nil {
			m.state.Status = "failed"
			m.state.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				m.state.Status = "cancelled"
				m.state.Error = "安装已取消；已完成组件和下载缓存保留，可继续安装"
			}
		} else {
			m.state.Status = "ready"
			if !wasComplete {
				m.state.Enabled = m.state.Engine.Path != "" && m.state.Templates.Path != ""
			}
		}
		if e := m.persistLocked(); e != nil {
			m.state.Status = "failed"
			m.state.Error = "保存安装状态失败: " + e.Error()
		} else {
			m.cleanupObsoleteLocked()
		}
	}()
	return nil
}

func (m *toolpackManager) reusableEngine(ctx context.Context, asset toolpackAsset) bool {
	installed := m.snapshot().Engine
	if installed.Version != asset.Version || !strings.EqualFold(installed.SHA256, asset.SHA256) || !withinToolpackRoot(m.root, installed.Path) {
		return false
	}
	info, err := os.Lstat(m.executable(installed))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	out, err := m.version(ctx, m.executable(installed))
	return err == nil && strings.Contains(out, strings.TrimPrefix(asset.Version, "v"))
}

func (m *toolpackManager) download(ctx context.Context, a toolpackAsset, dest string) error {
	return m.downloadResumable(ctx, a, dest)
}

func withinToolpackRoot(root, target string) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, abs)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func extractToolpackZip(ctx context.Context, archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	var expanded uint64
	if len(r.File) > 100000 {
		return errors.New("压缩包文件数超限")
	}
	for _, z := range r.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.ReplaceAll(z.Name, "\\", "/")
		if strings.Contains(name, ":") || strings.HasPrefix(name, "/") || z.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("不安全的压缩包条目: %s", z.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if !withinToolpackRoot(dest, target) {
			return errors.New("压缩包包含越界路径")
		}
		expanded += z.UncompressedSize64
		if expanded > 2<<30 {
			return errors.New("解压后大小超过 2 GiB")
		}
		if z.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		src, e := z.Open()
		if e != nil {
			return e
		}
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			src.Close()
			return e
		}
		_, e = io.Copy(f, io.LimitReader(src, int64(z.UncompressedSize64)+1))
		closeErr := f.Close()
		src.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *toolpackManager) installComponent(ctx context.Context, kind string, a toolpackAsset) error {
	if !toolpackVersionRE.MatchString(a.Version) {
		return errors.New("非法版本")
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.root, "install-")
	if err != nil {
		return err
	}
	defer func() {
		if withinToolpackRoot(m.root, tmp) {
			_ = os.RemoveAll(tmp)
		}
	}()
	m.mu.Lock()
	m.state.Stage = "下载并校验 " + a.Name
	m.state.Downloaded = 0
	m.state.Total = a.Size
	m.mu.Unlock()
	archive := filepath.Join(tmp, "package.zip")
	if err = m.download(ctx, a, archive); err != nil {
		return err
	}
	dir := filepath.Join(tmp, "files")
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	m.mu.Lock()
	m.state.Stage = "解压并检查 " + kind
	m.mu.Unlock()
	if err = extractToolpackZip(ctx, archive, dir); err != nil {
		return err
	}
	root := dir
	if kind == "engine" {
		bin := "nuclei"
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if _, err = os.Stat(filepath.Join(root, bin)); err != nil {
			return errors.New("发布包中没有 Nuclei 可执行文件")
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(filepath.Join(root, bin), 0700)
		}
		out, err := m.version(ctx, filepath.Join(root, bin))
		if err != nil {
			return fmt.Errorf("程序自检失败: %w", err)
		}
		if !strings.Contains(out, strings.TrimPrefix(a.Version, "v")) {
			return errors.New("程序版本与发布包版本不一致")
		}
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) != 1 || !entries[0].IsDir() {
			return errors.New("模板目录结构不符合官方发布包")
		}
		root = filepath.Join(dir, entries[0].Name())
		if _, err = os.Stat(filepath.Join(root, "http")); err != nil {
			return errors.New("模板包缺少 http 模板目录")
		}
	}
	// Unique destinations preserve versions referenced by in-flight executions.
	dest := filepath.Join(m.root, kind, a.Version+"-"+fmt.Sprint(time.Now().UnixNano()))
	if !withinToolpackRoot(m.root, dest) {
		return errors.New("安装路径越界")
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	if err = os.Rename(root, dest); err != nil {
		return err
	}
	component := toolpackComponent{Version: a.Version, Path: dest, SHA256: a.SHA256, Source: a.URL, InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind == "engine" {
		m.state.Engine = component
	} else {
		m.state.Templates = component
	}
	return m.persistLocked()
}

func (m *toolpackManager) version(ctx context.Context, bin string) (string, error) {
	if !withinToolpackRoot(m.root, bin) {
		return "", errors.New("程序路径不属于本实例")
	}
	if m.versionProbe != nil {
		return m.versionProbe(ctx, bin)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-version", "-duc")
	cmd.Env = m.environment()
	out, err := cmd.CombinedOutput()
	return string(out), err
}
func (m *toolpackManager) environment() []string {
	cache := filepath.Join(m.root, "runtime")
	_ = os.MkdirAll(cache, 0700)
	vars := map[string]string{"HOME": cache, "USERPROFILE": cache, "APPDATA": cache, "LOCALAPPDATA": cache, "XDG_CONFIG_HOME": cache, "XDG_CACHE_HOME": cache}
	var env []string
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if _, ok := vars[strings.ToUpper(k)]; !ok {
			env = append(env, v)
		}
	}
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	return env
}
func (m *toolpackManager) executable(c toolpackComponent) string {
	bin := "nuclei"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return filepath.Join(c.Path, bin)
}
func (m *toolpackManager) check(ctx context.Context) error {
	m.mu.Lock()
	if m.state.Engine.Path == "" {
		m.mu.Unlock()
		return errors.New("尚未安装引擎")
	}
	if m.cancel != nil {
		m.mu.Unlock()
		return errors.New("安装完成后再检测")
	}
	m.state.Running++
	bin := m.executable(m.state.Engine)
	m.mu.Unlock()
	out, err := m.version(ctx, bin)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Running--
	m.state.LastCheck = time.Now().UTC().Format(time.RFC3339)
	m.state.CheckOutput = out
	if err != nil {
		m.state.Error = "自检失败: " + err.Error()
	} else {
		m.state.Error = ""
	}
	_ = m.persistLocked()
	m.cleanupObsoleteLocked()
	return err
}

// Keep versions used by running calls; remove obsolete owned versions once idle.
// A cleanup error does not invalidate a successfully installed current version.
func (m *toolpackManager) cleanupObsoleteLocked() {
	if m.cancel != nil || m.state.Running > 0 {
		return
	}
	for kind, active := range map[string]string{"engine": m.state.Engine.Path, "templates": m.state.Templates.Path} {
		base := filepath.Join(m.root, kind)
		if !withinToolpackRoot(base, active) {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(base, entry.Name())
			if path != active && entry.IsDir() && withinToolpackRoot(base, path) {
				_ = os.RemoveAll(path)
			}
		}
	}
}

func (m *toolpackManager) uninstall() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil || m.state.Running > 0 {
		return errors.New("安装或调用正在进行，结束后才能卸载")
	}
	for _, name := range []string{"engine", "templates", "runtime", "cache"} {
		p := filepath.Join(m.root, name)
		if !withinToolpackRoot(m.root, p) {
			return errors.New("卸载路径越界")
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	m.state.Engine = toolpackComponent{}
	m.state.Templates = toolpackComponent{}
	m.state.Enabled = false
	m.state.Status = "not_installed"
	m.state.Error = ""
	m.state.CheckOutput = ""
	m.state.LastCheck = ""
	m.state.Downloaded = 0
	m.state.Total = 0
	return m.persistLocked()
}

func (s *Server) toolpackTools() []actool.CoreTool {
	m := s.toolpackManager()
	state := m.snapshot()
	if !state.Enabled || state.Engine.Path == "" || state.Templates.Path == "" {
		return nil
	}
	return []actool.CoreTool{roTool("nuclei_templates", "按关键词检索已安装的官方 Nuclei 模板，返回可交给 nuclei_scan 的相对路径。只查本地文件，不访问目标。", objSchema(map[string]any{"query": strParam("模板路径关键词，例如 technology 或已知产品名；不能为空")}, "query"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		q := strings.ToLower(strings.TrimSpace(in.Query))
		if q == "" {
			return actool.Errorf("请提供模板关键词"), nil
		}
		current := m.snapshot()
		if !current.Enabled || !withinToolpackRoot(m.root, current.Templates.Path) {
			return actool.Errorf("模板未就绪或工具已禁用"), nil
		}
		var results []string
		err := filepath.WalkDir(current.Templates.Path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(current.Templates.Path, p)
			rel = filepath.ToSlash(rel)
			if strings.HasSuffix(rel, ".yaml") && strings.Contains(strings.ToLower(rel), q) {
				results = append(results, rel)
				if len(results) >= 50 {
					return filepath.SkipAll
				}
			}
			return nil
		})
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		return jsonResult(map[string]any{"templates": results, "limit": 50})
	}), wrTool("nuclei_scan", "使用本实例安装的 Nuclei 对当前任务已授权的 URL 执行明确选定的模板。先用 nuclei_templates 查模板路径。模板匹配只是候选结果，仍需核对证据，不自动登记为确认漏洞。", objSchema(map[string]any{
		"targets":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "本任务范围内的 http/https URL，最多 32 个"},
		"templates": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "已安装模板包中的明确相对 YAML 文件路径，最多 16 个；不可传目录或任意命令"},
	}, "targets", "templates"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
		out, err := m.scan(ctx, raw)
		if err != nil {
			return actool.Errorf(err.Error() + "\n" + out), nil
		}
		return actool.Text(out), nil
	})}
}

func (m *toolpackManager) scan(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Targets   []string `json:"targets"`
		Templates []string `json:"templates"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	if len(in.Targets) < 1 || len(in.Targets) > 32 || len(in.Templates) < 1 || len(in.Templates) > 16 {
		return "", errors.New("请提供 1–32 个目标和 1–16 个明确模板")
	}
	m.mu.Lock()
	state := m.state
	if !state.Enabled || !withinToolpackRoot(m.root, state.Engine.Path) || !withinToolpackRoot(m.root, state.Templates.Path) {
		m.mu.Unlock()
		return "", errors.New("工具未安装、未启用或模板未就绪，请到工具页处理")
	}
	m.state.Running++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.state.Running--
		m.cleanupObsoleteLocked()
		m.mu.Unlock()
	}()
	args := []string{"-jsonl", "-silent", "-no-color", "-duc", "-ni", "-rl", "10", "-c", "2"}
	for _, target := range in.Targets {
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return "", errors.New("目标必须是 http/https URL")
		}
		args = append(args, "-u", target)
	}
	for _, name := range in.Templates {
		p := filepath.Join(state.Templates.Path, filepath.FromSlash(name))
		if !withinToolpackRoot(state.Templates.Path, p) || (filepath.Ext(p) != ".yaml" && filepath.Ext(p) != ".yml") {
			return "", errors.New("模板需为安装目录内的 YAML 文件")
		}
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("模板不可用: %s", name)
		}
		args = append(args, "-t", p)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.executable(state.Engine), args...)
	cmd.Dir = m.root
	cmd.Env = m.environment()
	buf := &toolpackOutput{limit: 2 << 20}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.String()
	if out == "" && err == nil {
		out = "所选模板运行完成，未产生匹配。未匹配不代表目标不存在其他问题。"
	}
	return out, err
}

type toolpackOutput struct {
	mu        sync.Mutex
	b         []byte
	limit     int
	truncated bool
}

func (b *toolpackOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.limit - len(b.b)
	if left > 0 {
		take := n
		if take > left {
			take = left
		}
		b.b = append(b.b, p[:take]...)
	}
	if n > left {
		b.truncated = true
	}
	return n, nil
}
func (b *toolpackOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := string(b.b)
	if b.truncated {
		s += "\n[输出达到 2 MiB 上限，已截断]"
	}
	return s
}
