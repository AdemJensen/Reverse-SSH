package tunnel

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type hostKeyPrompt struct {
	hostKey HostKey
	remote  net.Addr
	key     ssh.PublicKey
}

func (p *hostKeyPrompt) Error() string {
	return "SSH 服务器尚未受信任"
}

func readKnownHosts(path string) (ssh.HostKeyCallback, error) {
	callback, err := knownhosts.New(path)
	if errors.Is(err, os.ErrNotExist) {
		return knownhosts.New()
	}
	return callback, err
}

func hostKeyCallback(path string) (ssh.HostKeyCallback, error) {
	check, err := readKnownHosts(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取服务器指纹记录：%w", err)
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := check(host, remote, key)
		var keyError *knownhosts.KeyError
		if !errors.As(err, &keyError) {
			return err
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if len(keyError.Want) != 0 {
			return fmt.Errorf("服务器指纹与已保存的记录不符（收到 %s），请向服务器管理员核对", fingerprint)
		}
		return &hostKeyPrompt{
			hostKey: HostKey{Host: host, Type: key.Type(), Fingerprint: fingerprint},
			remote:  remote,
			key:     key,
		}
	}, nil
}

func (p *hostKeyPrompt) save(path string) error {
	check, err := readKnownHosts(path)
	if err != nil {
		return err
	}
	err = check(p.hostKey.Host, p.remote, p.key)
	if err == nil {
		return nil
	}
	var keyError *knownhosts.KeyError
	if !errors.As(err, &keyError) || len(keyError.Want) > 0 {
		return errors.New("该服务器已有不同的受信任指纹")
	}

	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	previous, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Replace the file after a complete write so a failed write leaves the existing pins usable.
	temp, err := os.CreateTemp(directory, "known-hosts-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err = temp.Write(previous); err != nil {
		return err
	}
	if len(previous) > 0 && previous[len(previous)-1] != '\n' {
		if _, err = temp.WriteString("\n"); err != nil {
			return err
		}
	}
	host := knownhosts.HashHostname(knownhosts.Normalize(p.hostKey.Host))
	if _, err = fmt.Fprintln(temp, knownhosts.Line([]string{host}, p.key)); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
