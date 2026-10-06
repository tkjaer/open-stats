package configure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
)

func TestEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "env")
	lines, token, err := readEnv(path)
	if err != nil || lines != nil || token != "" {
		t.Fatalf("missing file: %v %q %v", lines, token, err)
	}
	if err := writeEnv(path, []string{"# comment", "GOATCOUNTER_URL=http://127.0.0.1:8081", "GOATCOUNTER_TOKEN=" + strings.Repeat("ab", 24)}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi.Mode(), err)
	}
	lines, token, err = readEnv(path)
	if err != nil || token != strings.Repeat("ab", 24) || strings.Join(lines, "|") != "# comment|GOATCOUNTER_URL=http://127.0.0.1:8081" {
		t.Fatalf("%v %q %v", lines, token, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("left temp files: %v", entries)
	}

	for _, bad := range []string{"GOATCOUNTER_TOKEN=x'; drop table sites; --", "GOATCOUNTER_TOKEN=ABCDEF0123456789ABCDEF"} {
		os.WriteFile(path, []byte(bad+"\n"), 0o600)
		if _, _, err := readEnv(path); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSQLString(t *testing.T) {
	for _, ok := range []string{"how-the-internet-works.localhost", "open-stats export",
		"open-stats configure (temporary)", strings.Repeat("ab", 24), ""} {
		if q, err := sqlString(ok); err != nil || q != "'"+ok+"'" {
			t.Errorf("%q: %q, %v", ok, q, err)
		}
	}
	for _, bad := range []string{"x'", "x' or '1'='1", `x\`, "x\x00", "x\n", "x;", "x--y\"", "é"} {
		if q, err := sqlString(bad); err == nil {
			t.Errorf("%q: accepted as %s", bad, q)
		}
	}
	if got := sitesList([]int{3, 12, 1}); got != "'[3,12,1]'" {
		t.Errorf("sitesList: %s", got)
	}
	if got := sitesList(nil); got != "'[]'" {
		t.Errorf("sitesList(nil): %s", got)
	}
}

// A token in the env file that isn't 48 lowercase hex digits stops configure
// with a clear error before it runs anything against GoatCounter's database.
func TestRunRefusesBadToken(t *testing.T) {
	ps, err := project.LoadAll(projects.FS)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "goatcounter")
	// Records every call; answers queries with no rows.
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\necho '[]'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"x'; drop table sites; --",
		strings.Repeat("ab", 23) + "'a",
		strings.Repeat("ab", 10), // hex, but not a token configure made
		strings.Repeat("AB", 24),
	} {
		env := filepath.Join(dir, "env")
		os.WriteFile(env, []byte("GOATCOUNTER_URL=http://127.0.0.1:1\nGOATCOUNTER_TOKEN="+bad+"\n"), 0o600)
		var out strings.Builder
		err := Run(ps[0], Options{CLI: &goatcounter.CLI{Binary: fake, DB: "sqlite+/nonexistent"},
			URL: "http://127.0.0.1:1", EnvFile: env, AdminEmail: "a@example.org", Log: &out})
		if err == nil || !strings.Contains(err.Error(), "GOATCOUNTER_TOKEN in "+env+" is not 48 lowercase hex digits") {
			t.Errorf("%q: %v", bad, err)
		}
		if b, err := os.ReadFile(log); !os.IsNotExist(err) {
			t.Errorf("%q: goatcounter was run: %s", bad, b)
		}
	}
}
