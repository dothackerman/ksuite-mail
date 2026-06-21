// Package systemd renders the service and socket unit files that express the
// runtime deployment boundary (ARCH-DEP-003) with the hardening directives
// required by NFR-OPS-003.
//
// Rendering is pure text generation from the canonical layout; installing the
// units to disk is a separate, privileged concern handled by the bootstrap.
package systemd

import (
	"fmt"
	"strings"

	"github.com/dothackerman/ksuite-mail/internal/layout"
)

// Units holds the rendered unit file contents.
type Units struct {
	Service string
	Socket  string
}

// Render produces the service and socket units for the given socket access
// group. The access group gates who may connect to the socket; it must never
// be able to read the credential file or the cache (enforced by the layout
// modes, mirrored here in the socket boundary).
func Render(accessGroup string) (Units, error) {
	if strings.TrimSpace(accessGroup) == "" {
		return Units{}, fmt.Errorf("access group is required to render units")
	}
	return Units{
		Service: renderService(),
		Socket:  renderSocket(accessGroup),
	}, nil
}

func renderService() string {
	return fmt.Sprintf(`[Unit]
Description=ksuite-mail local Infomaniak K-Mail gateway daemon
Documentation=https://github.com/dothackerman/ksuite-mail
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s --config %s
User=%s
Group=%s

# Defense-in-depth hardening (NFR-OPS-003).
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s %s
RuntimeDirectory=ksuite-mail
RuntimeDirectoryMode=0750
UMask=0077

[Install]
WantedBy=multi-user.target
`,
		layout.InstalledDaem, layout.ConfigFile,
		layout.ServiceUser, layout.ServiceGroup,
		layout.StateDir, layout.RuntimeDir,
	)
}

func renderSocket(accessGroup string) string {
	return fmt.Sprintf(`[Unit]
Description=ksuite-mail daemon Unix domain socket

[Socket]
ListenStream=%s
SocketUser=%s
SocketGroup=%s
SocketMode=0660

[Install]
WantedBy=sockets.target
`,
		layout.SocketPath, layout.ServiceUser, accessGroup,
	)
}
