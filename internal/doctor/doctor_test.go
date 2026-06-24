package doctor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/api"
	"github.com/dothackerman/ksuite-mail/internal/doctor"
)

const validConfig = `[mail]
default_limit = 50
cache_ttl = "90d"

[[mail.accounts]]
id = "rs_info"
email = "info@example.com"
host = "imap.example.com"
port = 993
tls = true
username = "info@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "full"
folders = ["INBOX"]
`

const secretValue = "s3cr3t-value-DO-NOT-LEAK"

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// scenario lays out a temp deployment and returns doctor options pointing at it.
func scenario(t *testing.T, cfg, secretsJSON string, secretsMode os.FileMode) doctor.Options {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	secPath := filepath.Join(dir, "secrets.json")
	stateDir := filepath.Join(dir, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	writeFile(t, cfgPath, cfg, 0o640)
	writeFile(t, secPath, secretsJSON, secretsMode)
	return doctor.Options{ConfigPath: cfgPath, SecretsPath: secPath, StateDir: stateDir}
}

func find(t *testing.T, r api.DoctorReport, name string) api.Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %+v", name, r.Checks)
	return api.Check{}
}

func TestRunHealthyDeploymentPasses(t *testing.T) {
	opts := scenario(t, validConfig,
		`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+secretValue+`"}}`, 0o600)

	r := doctor.Run(opts)

	if !r.OK {
		t.Fatalf("expected healthy report, got %+v", r.Checks)
	}
	for _, name := range []string{"config_parse", "config_validate", "secrets_file", "cache", "credential:rs_info"} {
		if c := find(t, r, name); c.Status != api.CheckPass {
			t.Fatalf("check %q status = %q, want pass", name, c.Status)
		}
	}
	// Live IMAP connectivity is out of scope until the provider probe slice.
	if c := find(t, r, "imap_connectivity"); c.Status != api.CheckSkip {
		t.Fatalf("imap_connectivity status = %q, want skip", c.Status)
	}
}

func TestRunReportsMissingCredentialWithoutLeaking(t *testing.T) {
	opts := scenario(t, validConfig,
		`{"version":1,"secrets":{"/ksuite-mail/other/password":"`+secretValue+`"}}`, 0o600)

	r := doctor.Run(opts)

	if r.OK {
		t.Fatalf("expected failing report when credential is missing")
	}
	c := find(t, r, "credential:rs_info")
	if c.Status != api.CheckFail || c.Code != "credential_missing" {
		t.Fatalf("credential check = %+v, want fail/credential_missing", c)
	}
}

func TestRunFlagsUnsafeSecretsPermissions(t *testing.T) {
	opts := scenario(t, validConfig,
		`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+secretValue+`"}}`, 0o644)

	r := doctor.Run(opts)

	c := find(t, r, "secrets_file")
	if c.Status != api.CheckFail || c.Code != "secrets_unsafe_permissions" {
		t.Fatalf("secrets_file check = %+v, want fail/secrets_unsafe_permissions", c)
	}
}

func TestRunFlagsInvalidConfig(t *testing.T) {
	bad := strings.Replace(validConfig, `policy = "full"`, `policy = "bogus"`, 1)
	opts := scenario(t, bad,
		`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+secretValue+`"}}`, 0o600)

	r := doctor.Run(opts)

	if c := find(t, r, "config_validate"); c.Status != api.CheckFail {
		t.Fatalf("config_validate status = %q, want fail", c.Status)
	}
}

func TestRunNeverSerializesSecretValues(t *testing.T) {
	opts := scenario(t, validConfig,
		`{"version":1,"secrets":{"/ksuite-mail/rs_info/password":"`+secretValue+`"}}`, 0o600)

	r := doctor.Run(opts)

	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(b), secretValue) {
		t.Fatalf("doctor report leaked a secret value: %s", b)
	}
}
