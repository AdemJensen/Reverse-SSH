package control

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"reversessh/internal/tunnel"
)

func (c *Controller) recordAutoConnectError(message string) {
	c.diagnostics.Record(tunnel.Event{
		Time: time.Now(), Kind: "auto_connect_failed", Level: "error",
		State: tunnel.StateStopped, Message: message, Status: true,
	})
}

func (c *Controller) exportLogs(w http.ResponseWriter, r *http.Request) {
	if err := decode(r, &struct{}{}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if c.dialogs.ExportLogs == nil {
		writeError(w, http.StatusNotImplemented, errors.New("当前系统无法打开日志导出对话框。"))
		return
	}
	name := "ReverseSSH-Logs-" + time.Now().Format("2006-01-02-150405") + ".zip"
	path, err := c.dialogs.ExportLogs("", name)
	if err == nil && path != "" {
		err = c.diagnostics.Export(path)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("无法导出诊断日志：%w", err))
		return
	}
	writeJSON(w, exportResult{Saved: path != ""})
}

func (c *Controller) changeLogs(w http.ResponseWriter, r *http.Request) {
	var err error
	if r.Method == http.MethodPut {
		var req struct {
			MaxMiB int `json:"maxMiB"`
		}
		if err = decode(r, &req); err == nil {
			err = c.diagnostics.SetLimit(req.MaxMiB)
		}
	} else {
		if err = decode(r, &struct{}{}); err == nil {
			err = c.diagnostics.Clear()
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	writeJSON(w, c.state())
}
