package sshcommand

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"reversessh/internal/tunnel"
)

// Config contains the connection fields that can be imported from an SSH command.
type Config struct {
	Host     string
	SSHPort  int
	User     string
	Forwards []tunnel.Forward
	Warnings []string
}

// Parse reads an OpenSSH command as argument data without invoking a shell.
func Parse(command string) (Config, error) {
	words, err := shellWords(command)
	if err != nil {
		return Config{}, err
	}
	if len(words) == 0 || (words[0] != "ssh" && !strings.EqualFold(words[0], "ssh.exe")) {
		return Config{}, errors.New("请粘贴以 ssh 开头的命令。")
	}

	var config Config
	options := true
	for index := 1; index < len(words); index++ {
		word := words[index]
		if options && word == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(word, "-") && len(word) > 1 {
			consumed, err := config.readFlags(word[1:], words[index+1:])
			if err != nil {
				return Config{}, err
			}
			index += consumed
			continue
		}
		if config.Host != "" {
			return Config{}, errors.New("只能导入 SSH 转发参数，请去掉远端执行命令。")
		}
		if err := config.readDestination(word); err != nil {
			return Config{}, err
		}
	}
	if config.Host == "" {
		return Config{}, errors.New("SSH 命令缺少服务器地址。")
	}
	if len(config.Forwards) == 0 {
		return Config{}, errors.New("SSH 命令需要至少一条 TCP 反向转发参数 -R。")
	}
	if err := (tunnel.Config{Forwards: config.Forwards}).ValidateForwards(); err != nil {
		return Config{}, err
	}
	if config.SSHPort == 0 {
		config.SSHPort = 22
	}
	if config.User == "" {
		config.warn("命令未写明 SSH 用户名，请在应用中补填。")
	}
	return config, nil
}

func (c *Config) readFlags(flags string, remaining []string) (int, error) {
	for index := 0; index < len(flags); index++ {
		flag := flags[index]
		switch flag {
		case 'N', 'T', 'n', 'a', 'x', 'v', 'q':
			continue
		case 'A', 'X', 'Y', '4', '6', 'C', 'f', 'g', 'k', 'K', 'M', 't', 'y':
			c.warn(fmt.Sprintf("未导入 SSH 选项 -%c。", flag))
			continue
		case 'D', 'L', 'O', 'W', 'w', 's', 'G', 'Q', 'V':
			return 0, fmt.Errorf("不支持导入 SSH 选项 -%c。", flag)
		case 'B', 'b', 'c', 'E', 'e', 'F', 'I', 'i', 'J', 'l', 'm', 'o', 'p', 'R', 'S':
			value := flags[index+1:]
			consumed := 0
			if value == "" && len(remaining) > 0 {
				value = remaining[0]
				consumed = 1
			}
			if value == "" {
				return 0, fmt.Errorf("SSH 选项 -%c 缺少值。", flag)
			}
			return consumed, c.readValue(flag, value)
		default:
			return 0, fmt.Errorf("无法识别 SSH 选项 -%c。", flag)
		}
	}
	return 0, nil
}

func (c *Config) readValue(flag byte, value string) error {
	switch flag {
	case 'p':
		return c.setPort(value)
	case 'l':
		return c.setUser(value)
	case 'o':
		return c.readOption(value)
	case 'R':
		forward, err := parseForward(value)
		if err != nil {
			return err
		}
		c.Forwards = append(c.Forwards, forward)
	case 'i', 'I':
		c.warn(fmt.Sprintf("未导入 SSH 选项 -%c，请在应用中选择认证方式。", flag))
	case 'J':
		c.warn("未导入 SSH 跳板选项 -J。应用会直接连接目标服务器。")
	default:
		c.warn(fmt.Sprintf("未导入 SSH 选项 -%c。", flag))
	}
	return nil
}

func (c *Config) readOption(option string) error {
	separator := strings.IndexFunc(option, func(r rune) bool { return r == '=' || unicode.IsSpace(r) })
	if separator < 1 {
		return errors.New("-o 参数需要使用“名称=值”或带引号的“名称 值”。")
	}
	name := option[:separator]
	value := strings.TrimSpace(option[separator:])
	value = strings.TrimSpace(strings.TrimPrefix(value, "="))
	if value == "" {
		return fmt.Errorf("SSH 选项 %s 缺少值。", name)
	}
	switch strings.ToLower(name) {
	case "port":
		return c.setPort(value)
	case "user":
		return c.setUser(value)
	case "exitonforwardfailure":
		if strings.EqualFold(value, "yes") {
			return nil
		}
	case "sessiontype":
		if strings.EqualFold(value, "none") {
			return nil
		}
	case "requesttty", "clearallforwardings":
		if strings.EqualFold(value, "no") {
			return nil
		}
		if strings.EqualFold(name, "ClearAllForwardings") {
			return errors.New("请移除 ClearAllForwardings 选项，它会清除命令中的转发。")
		}
	case "remoteforward":
		return errors.New("请用 -R 参数提供每条远端转发，以便完整导入。")
	case "localforward", "dynamicforward", "stdioforward":
		return fmt.Errorf("不支持导入 SSH 选项 %s。", name)
	case "serveraliveinterval", "serveralivecountmax":
		c.warn(fmt.Sprintf("应用使用自身的 SSH 保活设置，未导入 %s。", name))
		return nil
	case "proxyjump", "proxycommand":
		c.warn(fmt.Sprintf("未导入 SSH 跳板选项 %s。应用会直接连接目标服务器。", name))
		return nil
	}
	c.warn(fmt.Sprintf("未导入 SSH 选项 %s。", name))
	return nil
}

func (c *Config) readDestination(destination string) error {
	user, host := "", destination
	if strings.HasPrefix(destination, "ssh://") {
		uri, err := url.Parse(destination)
		if err != nil || (uri.Path != "" && uri.Path != "/") || uri.Fragment != "" || uri.RawQuery != "" {
			return errors.New("SSH 服务器地址无效。")
		}
		host = uri.Hostname()
		if uri.User != nil {
			if _, present := uri.User.Password(); present {
				return errors.New("请勿在 SSH 地址中填写密码。")
			}
			user = uri.User.Username()
		}
		if uri.Port() != "" {
			if err := c.setPort(uri.Port()); err != nil {
				return err
			}
		}
	} else if at := strings.LastIndexByte(destination, '@'); at != -1 {
		user, host = destination[:at], destination[at+1:]
		if user == "" {
			return errors.New("SSH 服务器地址中的用户名为空。")
		}
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("SSH 命令缺少服务器地址。")
	}
	host, err := tunnel.NormalizeForwardEndpoint(host, 22)
	if err != nil {
		return errors.New("SSH 服务器地址必须是主机名或 IP 地址。")
	}
	if user != "" {
		if err := c.setUser(user); err != nil {
			return err
		}
	}
	c.Host = host
	return nil
}

func (c *Config) setPort(value string) error {
	port, err := parsePort(value, "SSH 端口")
	if err != nil {
		return err
	}
	if c.SSHPort != 0 && c.SSHPort != port {
		return fmt.Errorf("SSH 命令中的端口 %d 和 %d 冲突，请保留一个。", c.SSHPort, port)
	}
	c.SSHPort = port
	return nil
}

func (c *Config) setUser(value string) error {
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("SSH 用户名不能为空，也不能包含空白或控制字符。")
	}
	if c.User != "" && c.User != value {
		return fmt.Errorf("SSH 命令中的用户名 %q 和 %q 冲突，请保留一个。", c.User, value)
	}
	c.User = value
	return nil
}

func (c *Config) warn(message string) {
	if !slices.Contains(c.Warnings, message) {
		c.Warnings = append(c.Warnings, message)
	}
}

func parseForward(value string) (tunnel.Forward, error) {
	parts, err := splitForward(value)
	if err != nil {
		return tunnel.Forward{}, err
	}
	remote := "localhost"
	if len(parts) == 4 {
		remote = strings.TrimSpace(unbracket(parts[0]))
		parts = parts[1:]
		if remote == "" || remote == "*" {
			return tunnel.Forward{}, errors.New("请为 -R 指定明确的远端地址，例如 0.0.0.0 或 [::]。")
		}
	}
	local := strings.TrimSpace(unbracket(parts[1]))
	if local == "" {
		return tunnel.Forward{}, errors.New("-R 缺少本地服务地址。")
	}
	remotePort, err := parsePort(parts[0], "远端监听端口")
	if err != nil {
		return tunnel.Forward{}, err
	}
	localPort, err := parsePort(parts[2], "本地服务端口")
	if err != nil {
		return tunnel.Forward{}, err
	}
	return tunnel.Forward{RemoteHost: remote, RemotePort: remotePort, LocalHost: local, LocalPort: localPort}, nil
}

func splitForward(value string) ([]string, error) {
	invalid := errors.New("-R 仅支持 TCP 转发，格式为 [监听地址:]端口:本地地址:端口。IPv6 地址请加方括号。")
	var parts []string
	start := 0
	bracketed := false
	for index, character := range value {
		switch character {
		case '[':
			if bracketed || index != start {
				return nil, invalid
			}
			bracketed = true
		case ']':
			if !bracketed || (index+1 < len(value) && value[index+1] != ':') {
				return nil, invalid
			}
			bracketed = false
		case ':':
			if !bracketed {
				parts = append(parts, value[start:index])
				start = index + 1
			}
		}
	}
	parts = append(parts, value[start:])
	if bracketed || (len(parts) != 3 && len(parts) != 4) {
		return nil, invalid
	}
	return parts, nil
}

func unbracket(value string) string {
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return value[1 : len(value)-1]
	}
	return value
}

func parsePort(value, label string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s必须在 1–65535 之间。", label)
	}
	return port, nil
}
