package agent

import (
	"runtime"

	actool "github.com/Autumn-27/norma/tool"
)

func shellEnvironment() string {
	if runtime.GOOS == "windows" {
		return "本机 Windows；Bash 工具实际执行 powershell -NoProfile -NonInteractive -Command（Windows PowerShell 5.1）。不要使用 bash heredoc 或 &&；多行脚本先用 Write 保存，再用 python 文件名执行。使用 curl.exe 避免 PowerShell curl 别名；文本文件明确 UTF-8，路径含空格时加引号。HTTP(S) 代理与 CA 已通过环境变量配置；证书失败先核对 CA/代理，不默认关闭验证。"
	}
	return "本机系统：" + runtime.GOOS + "；Bash 使用系统 shell。HTTP(S) 代理与 CA 通过环境变量配置。"
}

type hostShellTool struct{ actool.CoreTool }

func (t *hostShellTool) Description() string { return shellEnvironment() }
func (t *hostShellTool) Prompt() string      { return shellEnvironment() }
func (t *hostShellTool) AcceptsRawInput() bool {
	v, ok := t.CoreTool.(interface{ AcceptsRawInput() bool })
	return ok && v.AcceptsRawInput()
}
func hostTools(base []actool.CoreTool, proxy, ca string) []actool.CoreTool {
	for i, tool := range base {
		if tool.Name() == "Bash" {
			base[i] = &hostShellTool{tool}
		}
	}
	return append(base, newObservedWebFetch(proxy, ca))
}

// Keep the Chat HTTP route explicit. Inherited proxies/NO_PROXY must not make
// a shell experiment silently take a different route from WebFetch.
func hostHTTPEnv(proxy, ca string) []string {
	env := proxyEnv(proxy, ca)
	if proxy == "" {
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			env = append(env, key+"=")
		}
		if ca != "" {
			for _, key := range []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"} {
				env = append(env, key+"="+ca)
			}
		}
	}
	return append(env, "NO_PROXY=", "no_proxy=")
}
