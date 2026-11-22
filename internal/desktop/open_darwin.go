package desktop

import (
	"context"
	"os/exec"
	"time"
)

func openBrowser(address string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/open", address).Run()
}
