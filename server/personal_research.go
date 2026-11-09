package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/agent"
)

// The conversation ID is resolved through the authenticated conversation route;
// callers cannot supply filesystem paths or choose another task's memory root.
func (s *Server) personalResearchState(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	if len(r.URL.Query().Get("q")) > 500 {
		writeErr(w, 400, "研究记录查询最多 500 字节")
		return
	}
	cfg, _, configured := s.personalProfileConfig(c)
	enabled := configured && (agent.IsDeepSeekFlash(cfg.Model) || isDefaultCTFAgent(c.AgentKey))
	root := filepath.Join(s.m.dir, "sessions", s.convBusyKey(c.ID))
	if _, err := os.Stat(root); os.IsNotExist(err) {
		writeJSON(w, 200, map[string]any{"enabled": enabled, "version": "1.2.0", "memory": nil})
		return
	}
	memory, err := agent.NewResearchMemory(root)
	if err != nil {
		writeErr(w, 500, "研究记录暂不可读："+err.Error())
		return
	}
	state, err := memory.Recall(r.Context(), agent.ResearchRecallInput{Query: r.URL.Query().Get("q"), Limit: 20, VerifyEvidence: r.URL.Query().Get("verify") == "true"})
	if err != nil {
		writeErr(w, 500, "研究记录读取失败："+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": enabled, "version": "1.2.0", "memory": state})
}
