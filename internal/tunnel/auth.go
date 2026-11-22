package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func prepareAuth(ctx context.Context, config Config, secrets Secrets) (ssh.AuthMethod, func(), error) {
	switch config.AuthMode {
	case AuthPassword:
		return ssh.Password(secrets.Password), func() {}, nil
	case AuthAgent:
		agentConn, err := connectAgent(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("无法连接 SSH Agent：%w", err)
		}
		if err = agentConn.SetDeadline(time.Now().Add(connectionTimeout)); err != nil {
			agentConn.Close()
			return nil, nil, err
		}
		cancel := context.AfterFunc(ctx, func() { agentConn.Close() })
		closeAgent := func() {
			cancel()
			agentConn.Close()
		}
		return ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers), closeAgent, nil
	default:
		signer, err := loadPrivateKey(config.PrivateKeyPath, secrets.KeyPassphrase)
		if err != nil {
			return nil, nil, err
		}
		return ssh.PublicKeys(signer), func() {}, nil
	}
}

func loadPrivateKey(path, passphrase string) (ssh.Signer, error) {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("无法定位用户目录：%w", err)
		}
		path = filepath.Join(home, path[2:])
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取 SSH 私钥：%w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, errors.New("SSH 私钥必须是小于 1 MB 的普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取 SSH 私钥：%w", err)
	}
	defer clear(data)
	signer, err := ssh.ParsePrivateKey(data)
	var missingPassphrase *ssh.PassphraseMissingError
	if errors.As(err, &missingPassphrase) {
		if passphrase == "" {
			return nil, errors.New("此私钥已加密，请输入私钥口令或通过 SSH Agent 使用它")
		}
		encodedPassphrase := []byte(passphrase)
		defer clear(encodedPassphrase)
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, encodedPassphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("无法解析 SSH 私钥，请检查文件格式和口令：%w", err)
	}
	return signer, nil
}
