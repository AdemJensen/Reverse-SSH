package control

import (
	"fmt"
	"net/http"

	"reversessh/internal/sshcommand"
)

type commandSettings struct {
	Host     string    `json:"host"`
	SSHPort  int       `json:"sshPort"`
	User     string    `json:"user"`
	Forwards []forward `json:"forwards"`
}

type commandImport struct {
	Settings commandSettings `json:"settings"`
	Warnings []string        `json:"warnings,omitempty"`
}

func importSSHCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Command string `json:"command"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config, err := sshcommand.Parse(req.Command)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(config.Forwards) > maxForwards {
		writeError(w, http.StatusBadRequest, fmt.Errorf("最多导入 %d 条端口转发。", maxForwards))
		return
	}
	settings := commandSettings{
		Host: config.Host, SSHPort: config.SSHPort, User: config.User,
		Forwards: make([]forward, len(config.Forwards)),
	}
	for i, route := range config.Forwards {
		settings.Forwards[i] = forward{
			RemoteBind: route.RemoteHost, RemotePort: route.RemotePort,
			LocalHost: route.LocalHost, LocalPort: route.LocalPort,
		}
	}
	writeJSON(w, commandImport{Settings: settings, Warnings: config.Warnings})
}
