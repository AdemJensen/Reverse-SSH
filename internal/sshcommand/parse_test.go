package sshcommand

import (
	"reflect"
	"strings"
	"testing"

	"reversessh/internal/tunnel"
)

func TestParseMultilineCommand(t *testing.T) {
	lines := []string{
		"ssh -NT",
		"  -p 7022",
		"  -o ExitOnForwardFailure=yes",
		"  -o ServerAliveInterval=30",
		"  -o ServerAliveCountMax=3",
		"  -R 0.0.0.0:10001:127.0.0.1:8080",
		"  tunnel@router.example",
	}
	for _, continuation := range []string{" \\\n", "\u00a0\\\\\u00a0\r\n"} {
		config, err := Parse(strings.Join(lines, continuation))
		if err != nil {
			t.Fatal(err)
		}
		if config.Host != "router.example" || config.User != "tunnel" || config.SSHPort != 7022 {
			t.Fatalf("unexpected SSH destination: %+v", config)
		}
		want := []tunnel.Forward{{RemoteHost: "0.0.0.0", RemotePort: 10001, LocalHost: "127.0.0.1", LocalPort: 8080}}
		if !reflect.DeepEqual(config.Forwards, want) {
			t.Fatalf("forwards = %+v, want %+v", config.Forwards, want)
		}
		if len(config.Warnings) != 2 || !strings.Contains(config.Warnings[0], "ServerAliveInterval") || !strings.Contains(config.Warnings[1], "ServerAliveCountMax") {
			t.Fatalf("unexpected warnings: %v", config.Warnings)
		}
	}
}

func TestParseMultipleForwardsAndIPv6(t *testing.T) {
	config, err := Parse(`ssh -NTp7022 -l operator -i 'my ssh key' -R'[::]:10001:[::1]:8080' -R 10002:localhost:9000 "[2001:db8::10]"`)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "2001:db8::10" || config.User != "operator" || config.SSHPort != 7022 {
		t.Fatalf("unexpected SSH destination: %+v", config)
	}
	want := []tunnel.Forward{
		{RemoteHost: "::", RemotePort: 10001, LocalHost: "::1", LocalPort: 8080},
		{RemoteHost: "localhost", RemotePort: 10002, LocalHost: "localhost", LocalPort: 9000},
	}
	if !reflect.DeepEqual(config.Forwards, want) {
		t.Fatalf("forwards = %+v, want %+v", config.Forwards, want)
	}
	if len(config.Warnings) != 1 || !strings.Contains(config.Warnings[0], "-i") {
		t.Fatalf("unexpected identity warning: %v", config.Warnings)
	}
}

func TestParseRepeatedOptionsAndURI(t *testing.T) {
	config, err := Parse(`ssh -o "Port = 7022" -oUser=operator -p 7022 operator@router.example -l operator -R10001:localhost:8080`)
	if err != nil {
		t.Fatal(err)
	}
	if config.SSHPort != 7022 || config.User != "operator" {
		t.Fatalf("unexpected repeated options: %+v", config)
	}
	uri, err := Parse(`ssh -R10001:localhost:8080 ssh://tunnel@[::1]:7022`)
	if err != nil || uri.Host != "::1" || uri.User != "tunnel" || uri.SSHPort != 7022 {
		t.Fatalf("unexpected result for SSH URI: %+v, %v", uri, err)
	}
}

func TestParseRejectsConflictingConnectionOptions(t *testing.T) {
	for _, test := range []struct {
		command string
		message string
	}{
		{`ssh -oPort=1111 -p2222 -R10001:localhost:8080 tunnel@router.example`, "端口 1111 和 2222 冲突"},
		{`ssh -p1111 -R10001:localhost:8080 ssh://tunnel@router.example:2222`, "端口 1111 和 2222 冲突"},
		{`ssh -oUser=alice -l bob -R10001:localhost:8080 router.example`, "用户名"},
		{`ssh -l alice -R10001:localhost:8080 bob@router.example`, "用户名"},
	} {
		if _, err := Parse(test.command); err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("Parse(%q) error = %v, want %q", test.command, err, test.message)
		}
	}
}

func TestParseRejectsCommandsAndUnsupportedRoutes(t *testing.T) {
	for _, command := range []string{
		`ssh -R10001:localhost:8080 tunnel@router.example uptime`,
		`ssh -R10001:localhost:8080 tunnel@router.example; uptime`,
		`ssh -R10001:localhost:8080 tunnel@router.example | other`,
		`ssh -R10001:localhost:8080 "tunnel@$(hostname)"`,
		"ssh -R10001:localhost:8080 tunnel@router.example\x00",
		"ssh -R10001:localhost:8080 tunnel@router.example\nssh another.example",
		`ssh -R10001:localhost:8080 "tunnel@router.example`,
		`ssh -R10001 tunnel@router.example`,
		`ssh -R' :10001:localhost:8080' tunnel@router.example`,
		`ssh -R '*:10001:localhost:8080' tunnel@router.example`,
		`ssh -R/tmp/remote.sock:/tmp/local.sock tunnel@router.example`,
		`ssh -R10001:localhost:99999 tunnel@router.example`,
		`ssh -R0.0.0.0:10001:localhost:8080 -R0.0.0.0:10001:localhost:8081 tunnel@router.example`,
		`ssh -R10001:localhost:8080 -D1080 tunnel@router.example`,
		`ssh -oClearAllForwardings=yes -R10001:localhost:8080 tunnel@router.example`,
		`ssh -R10001:localhost:8080 ssh://tunnel:password@router.example`,
	} {
		if _, err := Parse(command); err == nil {
			t.Errorf("accepted unsupported command %q", command)
		}
	}
}
