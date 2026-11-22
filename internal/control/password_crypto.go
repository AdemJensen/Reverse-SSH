package control

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"reversessh/internal/tunnel"
)

// Keep this key stable across builds so existing profiles remain readable.
var profilePasswordKey = [32]byte{
	0x02, 0x3c, 0x30, 0x3a, 0x1a, 0x88, 0xf1, 0x76,
	0xe1, 0xdb, 0x30, 0x47, 0x5f, 0x53, 0x0b, 0x83,
	0x3f, 0x78, 0x8f, 0x40, 0x1e, 0xfc, 0x1a, 0x55,
	0x23, 0x18, 0xe2, 0xf5, 0x80, 0x47, 0x9c, 0x00,
}

type sshCredentialIdentity struct {
	Host string
	Port int
	User string
}

func (p profile) credentialIdentity() sshCredentialIdentity {
	host := strings.TrimSpace(p.Host)
	if host != "" {
		normalized, err := tunnel.NormalizeForwardEndpoint(host, p.SSHPort)
		if err == nil {
			host = normalized
		} else {
			host = strings.ToLower(host)
		}
	}
	return sshCredentialIdentity{Host: host, Port: p.SSHPort, User: strings.TrimSpace(p.User)}
}

func (p profile) passwordAAD() []byte {
	identity, _ := json.Marshal(p.credentialIdentity())
	return append([]byte("reverse-ssh/password/v1\x00"), identity...)
}

func profilePasswordCipher() (cipher.AEAD, error) {
	block, err := aes.NewCipher(profilePasswordKey[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func encryptProfilePassword(p profile) (json.RawMessage, error) {
	aead, err := profilePasswordCipher()
	if err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, nil, []byte(p.SavedPassword), p.passwordAAD())
	return json.Marshal(base64.StdEncoding.EncodeToString(sealed))
}

func decryptProfilePassword(p profile, raw json.RawMessage) (string, error) {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	aead, err := profilePasswordCipher()
	if err != nil {
		return "", err
	}
	plaintext, err := aead.Open(nil, nil, sealed, p.passwordAAD())
	if err != nil {
		return "", err
	}
	if len(plaintext) == 0 {
		return "", errors.New("saved SSH password is empty")
	}
	return string(plaintext), nil
}
