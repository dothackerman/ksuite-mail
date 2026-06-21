package bootstrap

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dothackerman/ksuite-mail/internal/layout"
)

// --- test doubles ---------------------------------------------------------

type fakeUsers struct {
	users        map[string]bool
	groups       map[string]bool
	primaryGroup map[string]string
	created      []string
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{
		users:        map[string]bool{},
		groups:       map[string]bool{"oriol": true, "ksuite-mail": true},
		primaryGroup: map[string]string{"oriol": "oriol"},
	}
}

func (f *fakeUsers) UserExists(name string) (bool, error)  { return f.users[name], nil }
func (f *fakeUsers) GroupExists(name string) (bool, error) { return f.groups[name], nil }
func (f *fakeUsers) EnsureSystemUser(name, _, _ string) error {
	f.users[name] = true
	f.groups[name] = true
	f.created = append(f.created, name)
	return nil
}
func (f *fakeUsers) PrimaryGroupName(user string) (string, error) { return f.primaryGroup[user], nil }

type fakeChowner struct {
	intents map[string]layout.Owner // canonical path -> owner
	root    string
}

func (c *fakeChowner) Chown(path string, owner layout.Owner) error {
	// Record against the canonical (un-rooted) path for stable assertions.
	rel := path
	if c.root != "" {
		rel = "/" + strings.TrimPrefix(strings.TrimPrefix(path, c.root), "/")
	}
	c.intents[filepath.Clean(rel)] = owner
	return nil
}

type fakePrompter struct {
	secret string
	calls  int
}

func (p *fakePrompter) PromptSecret(string) ([]byte, error) {
	p.calls++
	return []byte(p.secret), nil
}

func newDeps(root string, prompter Prompter) (Deps, *fakeChowner, *fakeUsers) {
	users := newFakeUsers()
	chown := &fakeChowner{intents: map[string]layout.Owner{}, root: root}
	return Deps{Users: users, Chown: chown, Prompt: prompter}, chown, users
}

// --- tests ----------------------------------------------------------------

func TestRunPreparesBoundaryWithHardenedModes(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	deps, chown, users := newDeps(root, nil)

	res, err := Run(Options{Root: root, InvokingUser: "oriol", Out: &out}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !res.ServiceUserCreated || len(users.created) != 1 || users.created[0] != layout.ServiceUser {
		t.Fatalf("service user not created as expected: %+v", users.created)
	}
	if res.AccessGroup != "oriol" {
		t.Fatalf("access group = %q, want oriol", res.AccessGroup)
	}

	// Exact directory modes (umask must not relax them).
	assertMode(t, filepath.Join(root, layout.ConfigDir), 0o750)
	assertMode(t, filepath.Join(root, layout.StateDir), 0o700)
	assertMode(t, filepath.Join(root, layout.RuntimeDir), 0o750)

	// Exact file modes: secrets readable only by owner, config group-readable.
	assertMode(t, filepath.Join(root, layout.ConfigFile), 0o640)
	assertMode(t, filepath.Join(root, layout.SecretsFile), 0o600)

	// Ownership intents must match the documented boundary (ARCH-DEP-001).
	wantOwners := map[string]layout.Owner{
		layout.ConfigDir:   {User: "root", Group: "ksuite-mail"},
		layout.StateDir:    {User: "ksuite-mail", Group: "ksuite-mail"},
		layout.RuntimeDir:  {User: "ksuite-mail", Group: "oriol"},
		layout.ConfigFile:  {User: "root", Group: "ksuite-mail"},
		layout.SecretsFile: {User: "ksuite-mail", Group: "root"},
	}
	for path, want := range wantOwners {
		got, ok := chown.intents[path]
		if !ok {
			t.Errorf("no chown recorded for %s", path)
			continue
		}
		if got != want {
			t.Errorf("owner of %s = %s:%s, want %s:%s", path, got.User, got.Group, want.User, want.Group)
		}
	}

	// Cache directory and credential file must not be readable by the socket
	// access group: neither is group-owned by it, and neither grants group read
	// to a non-service group.
	if g := chown.intents[layout.SecretsFile].Group; g == "oriol" {
		t.Errorf("secrets file must not be group-owned by the access group")
	}
	if g := chown.intents[layout.StateDir].Group; g == "oriol" {
		t.Errorf("cache directory must not be group-owned by the access group")
	}
}

func TestRunSeedsAccountAndKeepsSecretOutOfOutput(t *testing.T) {
	root := t.TempDir()
	const secret = "sup3r-s3cr3t-app-pw-Z9x"
	var out bytes.Buffer
	deps, _, _ := newDeps(root, &fakePrompter{secret: secret})

	res, err := Run(Options{
		Root:         root,
		InvokingUser: "oriol",
		Out:          &out,
		Account: &AccountSeed{
			ID:       "rs_info",
			Email:    "info@regenerativ.ch",
			Host:     "mail.infomaniak.com",
			Port:     993,
			TLS:      true,
			Username: "info@regenerativ.ch",
			Policy:   "full",
			Folders:  []string{"INBOX", "Sent"},
		},
	}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.AccountAdded != "rs_info" {
		t.Fatalf("AccountAdded = %q", res.AccountAdded)
	}

	// The secret must live only in the protected secrets file.
	key := secretKey("rs_info")
	store, err := loadSecretStore(filepath.Join(root, layout.SecretsFile))
	if err != nil {
		t.Fatalf("loadSecretStore: %v", err)
	}
	if store.Secrets[key] != secret {
		t.Fatalf("secret not stored under %q", key)
	}
	assertMode(t, filepath.Join(root, layout.SecretsFile), 0o600)

	// Redaction: the secret must never appear in human-facing output.
	if strings.Contains(out.String(), secret) {
		t.Fatalf("secret leaked into init output:\n%s", out.String())
	}

	// The config must reference the secret indirectly, never inline.
	cfgData, err := os.ReadFile(filepath.Join(root, layout.ConfigFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(cfgData), secret) {
		t.Fatalf("secret leaked into config file")
	}
	if !strings.Contains(string(cfgData), key) {
		t.Fatalf("config does not contain the expected password_ref id %q", key)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	if _, err := Run(Options{Root: root, InvokingUser: "oriol"}, deps); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second run validates the existing boundary instead of failing.
	deps2, _, _ := newDeps(root, nil)
	res, err := Run(Options{Root: root, InvokingUser: "oriol"}, deps2)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res.ConfigCreated || res.SecretsCreated {
		t.Fatalf("second run should validate, not recreate: %+v", res)
	}
}

func TestRunInstallsUnits(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	res, err := Run(Options{Root: root, InvokingUser: "oriol", Units: UnitsInstall}, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.UnitsInstalled || len(res.UnitPaths) != 2 {
		t.Fatalf("units not installed: %+v", res)
	}
	svc, err := os.ReadFile(filepath.Join(root, layout.SystemdUnitDir, layout.ServiceUnit))
	if err != nil {
		t.Fatalf("read service unit: %v", err)
	}
	if !strings.Contains(string(svc), "User=ksuite-mail") {
		t.Fatalf("installed service unit missing hardened User directive")
	}
}

func TestRunRequiresAccessGroupResolution(t *testing.T) {
	root := t.TempDir()
	deps, _, _ := newDeps(root, nil)
	// No AccessGroup and no InvokingUser -> cannot resolve.
	if _, err := Run(Options{Root: root}, deps); err == nil {
		t.Fatal("expected error when access group cannot be resolved")
	}
}

func TestRunRejectsInvalidAccountBeforePrompting(t *testing.T) {
	root := t.TempDir()
	prompter := &fakePrompter{secret: "x"}
	deps, _, _ := newDeps(root, prompter)
	_, err := Run(Options{
		Root:         root,
		InvokingUser: "oriol",
		Account:      &AccountSeed{ID: "bad", Policy: "domain"}, // missing fields + no domains
	}, deps)
	if err == nil {
		t.Fatal("expected validation error for bad account seed")
	}
	if prompter.calls != 0 {
		t.Fatalf("prompter must not be called for an invalid account; calls=%d", prompter.calls)
	}
}

// --- helpers --------------------------------------------------------------

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %#o, want %#o", path, got, want)
	}
}
