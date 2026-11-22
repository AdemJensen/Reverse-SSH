package control

import (
	"errors"
	"net/http"

	"reversessh/internal/tunnel"
)

func (p profile) hasSavedPassword() bool {
	return p.Auth == tunnel.AuthPassword && p.RememberPassword && p.SavedPassword != ""
}

func (c *Controller) updateProfile(r *http.Request) error {
	var req struct {
		profile
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	req.normalize()
	if err := c.applyPassword(&req.profile, req.Password); err != nil {
		return err
	}
	if err := req.validateStorage(); err != nil {
		return err
	}
	return c.save(req.profile)
}

func (c *Controller) applyPassword(p *profile, entered string) error {
	if p.Auth != tunnel.AuthPassword {
		return nil
	}
	if !p.RememberPassword {
		p.AutoConnect = false
		return nil
	}
	if entered == "" {
		entered = c.profile.passwordFor(*p)
	}
	if entered == "" {
		return errors.New("请输入要保存的 SSH 密码。")
	}
	p.SavedPassword = entered
	p.SavedPasswordUnavailable = false
	return nil
}

func (p profile) passwordFor(draft profile) string {
	if draft.Auth == tunnel.AuthPassword && draft.RememberPassword && p.hasSavedPassword() &&
		draft.credentialIdentity() == p.credentialIdentity() {
		return p.SavedPassword
	}
	return ""
}
