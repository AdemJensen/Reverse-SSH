package control

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"reversessh/internal/tunnel"
)

type importResult struct {
	Imported bool          `json:"imported"`
	State    responseState `json:"state"`
}

type exportResult struct {
	Saved bool `json:"saved"`
}

func (c *Controller) importConfiguration(w http.ResponseWriter, r *http.Request) {
	if err := decode(r, &struct{}{}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.dialogs.Import == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开导入文件选择器。"))
		return
	}
	blocked := errors.New("请先断开隧道，再导入配置。")
	if c.connectionInProgress() {
		writeError(w, http.StatusConflict, blocked)
		return
	}
	path, err := c.dialogs.Import("")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if path == "" {
		c.mu.Lock()
		defer c.mu.Unlock()
		writeJSON(w, importResult{State: c.state()})
		return
	}
	p, err := readProfile(path, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connectionInProgress() {
		writeError(w, http.StatusConflict, blocked)
		return
	}
	if err := c.save(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c.appMessage = ""
	c.pending = nil
	c.closePreviews()
	c.manager.Stop()
	writeJSON(w, importResult{Imported: true, State: c.state()})
}

func (c *Controller) exportConfiguration(w http.ResponseWriter, r *http.Request) {
	var p profile
	if err := decode(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p.normalize()
	c.mu.Lock()
	p.SavedPassword = c.profile.passwordFor(p)
	c.mu.Unlock()
	if p.Auth == tunnel.AuthPassword && !p.hasSavedPassword() {
		p.RememberPassword, p.AutoConnect = false, false
	}
	if err := p.validateStorage(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.dialogs.Export == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开导出文件选择器。"))
		return
	}
	path, err := c.dialogs.Export("", "Reverse-SSH.json")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if path == "" {
		writeJSON(w, exportResult{})
		return
	}
	if filepath.Clean(path) == filepath.Clean(c.profilePath) || filesAreSame(path, c.profilePath) {
		writeError(w, http.StatusBadRequest, errors.New("请为导出文件选择其他位置，以免覆盖应用内的配置。"))
		return
	}
	if err := saveProfile(path, p); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("无法导出配置：%w", err))
		return
	}
	writeJSON(w, exportResult{Saved: true})
}

func filesAreSame(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}
