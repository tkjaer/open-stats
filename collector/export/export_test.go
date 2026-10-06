package export

import (
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/collector/goatcounter"
	"github.com/tkjaer/open-stats/internal/project"
	"github.com/tkjaer/open-stats/projects"
)

func day(s string) time.Time {
	d, _ := time.Parse(time.DateOnly, s)
	return d
}

func TestCheckExportable(t *testing.T) {
	tests := []struct {
		week, today, err string
	}{
		{"2026-W40", "2026-10-05", ""},
		{"2026-W37", "2026-10-05", ""}, // starts 7 Sep, 28 days ago
		{"2026-W41", "2026-10-05", "not over"},
		{"2026-W40", "2026-10-04", "not over"},
		{"2026-W36", "2026-10-05", "retention"}, // starts 31 Aug, 35 days ago
		{"2026-W99", "2026-10-05", "no such week"},
	}
	for _, tt := range tests {
		_, err := CheckExportable(tt.week, day(tt.today))
		if (err == nil) != (tt.err == "") || (err != nil && !strings.Contains(err.Error(), tt.err)) {
			t.Errorf("%s on %s: got %v, want %q", tt.week, tt.today, err, tt.err)
		}
	}
}

func TestSnapshot(t *testing.T) {
	ps, _ := project.LoadAll(projects.FS, "how-the-internet-works")
	p := ps[0]
	site := &goatcounter.Site{Cname: "how-the-internet-works.localhost", Settings: goatcounter.SiteSettings{
		Collect: 16, DataRetention: 31, Public: "private", Secret: "s3cret",
		IgnoreIPs: []byte(`"1.2.3.4"`), AllowEmbed: []byte(`""`)}}
	snap := Snapshot(p, site, "v2.7.0")
	if strings.Join(snap.GoatCounter.Collect, ",") != "country" {
		t.Errorf("collect %v", snap.GoatCounter.Collect)
	}
	if pr := Problems(p, snap); len(pr) != 0 {
		t.Errorf("problems %v", pr)
	}
	site.Settings.Collect, site.Settings.Public = 190, "public"
	if pr := Problems(p, Snapshot(p, site, "v2.7.0")); len(pr) != 2 {
		t.Errorf("problems %v", pr)
	}
	site.Settings.Collect, site.Settings.Public, site.Settings.AllowEmbed = 16, "private", []byte(`["example.org"]`)
	if pr := Problems(p, Snapshot(p, site, "v2.7.0")); len(pr) != 1 {
		t.Errorf("problems %v", pr)
	}
}
