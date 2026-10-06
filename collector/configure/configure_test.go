package configure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
