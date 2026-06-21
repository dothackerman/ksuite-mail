package systemd

import (
	"strings"
	"testing"
)

func TestRenderRequiresAccessGroup(t *testing.T) {
	if _, err := Render("  "); err == nil {
		t.Fatal("expected error for empty access group")
	}
}

func TestRenderServiceHardening(t *testing.T) {
	u, err := Render("oriol")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// NFR-OPS-003 hardening directives must all be present.
	for _, want := range []string{
		"User=ksuite-mail",
		"Group=ksuite-mail",
		"NoNewPrivileges=true",
		"PrivateTmp=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"UMask=0077",
		"ExecStart=/usr/local/bin/ksuite-maild --config /etc/ksuite-mail/config.toml",
		"ReadWritePaths=/var/lib/ksuite-mail /run/ksuite-mail",
	} {
		if !strings.Contains(u.Service, want) {
			t.Errorf("service unit missing %q\n%s", want, u.Service)
		}
	}
}

func TestRenderSocketBoundary(t *testing.T) {
	u, err := Render("oriol")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"ListenStream=/run/ksuite-mail/ksuite-mail.sock",
		"SocketUser=ksuite-mail",
		"SocketGroup=oriol",
		"SocketMode=0660",
	} {
		if !strings.Contains(u.Socket, want) {
			t.Errorf("socket unit missing %q\n%s", want, u.Socket)
		}
	}
	// The access group must gate the socket, never own the daemon's data: the
	// credential file is owned by the service user, not the access group.
	if strings.Contains(u.Socket, "secrets.json") {
		t.Errorf("socket unit must not reference the credential file")
	}
}
