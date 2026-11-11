package server

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// maxChatUpload caps a single chat-attachment upload request (memory + spill).
const maxChatUpload = 128 << 20 // 128 MiB

// safeChatID guards the {id} path segment against traversal — task ids are numeric,
// session conversation IDs are resolved separately below.
var safeChatID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// chatAttachment is one uploaded file as the frontend + agent see it. Path is relative
// to the chat's working dir (e.g. "uploads/report.txt"), which is the agent's CWD, so
// it can Read/Bash the file directly; Name/Size drive the UI card.
type chatAttachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Abs 是落盘的绝对路径(m.dir 已是绝对)。建任务前暂存(scope=staging)时前端要用它把
	// 提示词写进描述;task/session 走 composeAgentMessage 在后端拼路径,不依赖此字段。
	Abs string `json:"abs,omitempty"`
}

// chatUpload implements method-1 file support: it saves one or more files into a chat's
// working dir under uploads/, so the agent opens them with its existing Read/Bash tools
// and the sent message carries their paths. No LLM-layer change, no multimodal.
//
// POST /api/chat/upload?scope=task|session|staging&id=<id>, multipart field "file"
// (repeatable). Returns {attachments:[{name,path,size,abs}]}. Target dir mirrors the
// agent CWD layout:
//
//	scope=task    → <workDir>/tasks/<id>/uploads/
//	scope=session → <workDir>/sessions/<id>/uploads/
//	scope=staging → <workDir>/drafts/<id>/uploads/   (建任务前暂存:任务尚无 ID,
//	                文件先落这里,前端按返回的 abs 绝对路径写进任务描述)
func (s *Server) chatUpload(w http.ResponseWriter, r *http.Request) {
	var sub string
	taskScoped := false
	switch r.URL.Query().Get("scope") {
	case "task":
		sub = "tasks"
		taskScoped = true
	case "session":
		sub = "sessions"
	case "staging":
		sub = "drafts"
	default:
		writeErr(w, 400, "scope 必须是 task / session / staging")
		return
	}
	id := r.URL.Query().Get("id")
	if !safeChatID.MatchString(id) {
		writeErr(w, 400, "非法 id")
		return
	}
	if sub == "sessions" {
		conversationID, err := strconv.ParseInt(strings.TrimPrefix(id, "conv-"), 10, 64)
		if err != nil || conversationID <= 0 {
			writeErr(w, 400, "非法会话 id")
			return
		}
		if s.m.pg == nil {
			writeErr(w, 503, "数据库不可用")
			return
		}
		c, err := s.m.pg.GetConversation(conversationID)
		if err != nil || c == nil {
			writeErr(w, 404, "会话不存在")
			return
		}
		id = s.convBusyKey(conversationID)
	}
	if taskScoped {
		if s.m.ResolveTask(id) == nil {
			writeErr(w, 404, "task not found")
			return
		}
		if !s.engine.beginTaskOperation(id) {
			writeErr(w, http.StatusConflict, "任务正在删除，无法上传附件")
			return
		}
		defer s.engine.decInflight(id)
	}
	dir := filepath.Join(s.m.dir, sub, id, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, 500, "建目录失败: "+err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChatUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "解析上传失败或超出大小限制: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, "缺少上传文件(表单字段 file)")
		return
	}
	out := make([]chatAttachment, 0, len(files))
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename)
		if !validUploadName(name) {
			writeErr(w, 400, "非法附件名称")
			return
		}
		dest, err := saveUniqueChatUpload(dir, name, hdr)
		if err != nil {
			writeErr(w, 500, "保存失败: "+err.Error())
			return
		}
		base := filepath.Base(dest)
		out = append(out, chatAttachment{Name: base, Path: "uploads/" + base, Size: hdr.Size, Abs: dest})
	}
	writeJSON(w, 200, map[string]any{"attachments": out})
}

func (s *Server) conversationWorkDir(id int64) string {
	return filepath.Join(s.m.dir, "sessions", s.convBusyKey(id))
}

func validUploadName(name string) bool {
	return filepath.IsLocal(name) && name != "." && !strings.ContainsAny(name, "/\\:\x00\r\n")
}

// O_EXCL reserves a filename before copying, including concurrent uploads.
func saveUniqueChatUpload(dir, name string, hdr *multipart.FileHeader) (string, error) {
	src, err := hdr.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	return copyUniqueUpload(dir, name, src)
}

func copyUniqueUpload(dir, name string, src io.Reader) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		dest := filepath.Join(dir, candidate)
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(out, src)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(dest) // only this call's newly reserved file
			if copyErr != nil {
				return "", copyErr
			}
			return "", closeErr
		}
		return dest, nil
	}
}

// Validate server-owned paths and metadata before accepting a message. Old
// numeric-session uploads are copied on demand; original files are retained.
func (s *Server) resolveConversationAttachments(id int64, in []chatAttachment) ([]chatAttachment, error) {
	out := make([]chatAttachment, 0, len(in))
	for _, a := range in {
		name := strings.TrimPrefix(a.Path, "uploads/")
		if a.Path != "uploads/"+name || !validUploadName(name) {
			return nil, fmt.Errorf("非法附件路径，请重新选择附件")
		}
		base := s.conversationWorkDir(id)
		root, err := os.OpenRoot(base)
		var f *os.File
		if err == nil {
			f, err = root.Open(a.Path)
			root.Close()
		}
		if os.IsNotExist(err) {
			legacy, openErr := os.OpenRoot(filepath.Join(s.m.dir, "sessions", strconv.FormatInt(id, 10), "uploads"))
			if openErr == nil {
				old, oldErr := legacy.Open(name)
				if oldErr == nil {
					info, statErr := old.Stat()
					if statErr == nil && info.Mode().IsRegular() {
						var dest string
						dest, err = copyUniqueUpload(filepath.Join(base, "uploads"), name, old)
						if err == nil {
							name = filepath.Base(dest)
							a.Path = "uploads/" + name
							f, err = os.Open(dest)
						}
					}
					old.Close()
				}
				legacy.Close()
			}
		}
		if err != nil || f == nil {
			return nil, fmt.Errorf("附件 %s 不存在或无法读取，请重新上传", name)
		}
		info, err := f.Stat()
		f.Close()
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("附件 %s 不是可读取的文件", name)
		}
		out = append(out, chatAttachment{Name: name, Path: a.Path, Size: info.Size(), Abs: filepath.Join(base, a.Path)})
	}
	return out, nil
}

// composeAgentMessage appends an attachment manifest to the user's message so the agent
// knows which files were uploaded and where to Read them. baseDir is the agent's working
// dir (its CWD); we emit ABSOLUTE paths (baseDir + relative) so the agent can Read/Bash
// them unambiguously regardless of how it interprets relative paths.
func composeAgentMessage(msg string, atts []chatAttachment, baseDir string) string {
	if len(atts) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	b.WriteString("\n\n【用户上传的附件】(绝对路径，需要时用 Read/Bash 查看)：")
	for _, a := range atts {
		fmt.Fprintf(&b, "\n- %s（%s）", filepath.Join(baseDir, a.Path), humanBytes(a.Size))
	}
	return b.String()
}

// userActivityWithAttachments builds the persisted 'user' activity. With attachments,
// Detail holds JSON {text, attachments} so the transcript renders text + attachment
// cards; Summary stays the plain text (the list payload omits Detail, lazy-loaded).
func userActivityWithAttachments(worker, text string, atts []chatAttachment) db.Activity {
	a := db.Activity{Worker: worker, Kind: "user", Summary: text}
	if len(atts) > 0 {
		blob, _ := json.Marshal(map[string]any{"text": text, "attachments": atts})
		a.Detail = string(blob)
	}
	return a
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
