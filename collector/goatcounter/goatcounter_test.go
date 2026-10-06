// SPDX-License-Identifier: AGPL-3.0-or-later

package goatcounter

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tkjaer/open-stats/internal/isoweek"
)

// fakeLocations answers like GoatCounter's /api/v0/stats/locations: n
// countries a day, sorted, paged with limit (at most 100) and offset.
func fakeLocations(t *testing.T, n int, broken string) *API {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/v0/stats/total":
			fmt.Fprint(w, `{"stats":[]}`)
		case "/api/v0/stats/hits":
			fmt.Fprint(w, `{"hits":[],"more":false}`)
		case "/api/v0/stats/locations":
			limit, _ := strconv.Atoi(q.Get("limit"))
			offset, _ := strconv.Atoi(q.Get("offset"))
			if limit < 1 || limit > 100 {
				http.Error(w, "bad limit", 400)
				return
			}
			type stat struct {
				ID    string `json:"id"`
				Count int    `json:"count"`
			}
			var page []stat
			for i := offset; i < min(n, offset+limit); i++ {
				page = append(page, stat{fmt.Sprintf("C%03d", i), n - i})
			}
			more := offset+limit < n
			switch broken {
			case "repeat": // ignores offset
				page = page[:0]
				for i := 0; i < min(n, limit); i++ {
					page = append(page, stat{fmt.Sprintf("C%03d", i), n - i})
				}
			case "endless":
				more = true
			}
			json.NewEncoder(w).Encode(map[string]any{"stats": page, "more": more})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	a := NewAPI(srv.URL, "token")
	a.Pause = 0
	return a
}

func TestWeekPagesThroughCountries(t *testing.T) {
	days, _ := isoweek.Days("2026-W40")
	for _, n := range []int{0, 1, 99, 100, 101, 250} {
		raw, err := fakeLocations(t, n, "").Week("x.localhost", []string{"en"}, days)
		if err != nil {
			t.Fatalf("%d countries: %v", n, err)
		}
		for _, c := range raw.Countries {
			if len(c) != n {
				t.Errorf("%d countries: got %d", n, len(c))
			}
			if n > 0 && (c["C000"] != n || c[fmt.Sprintf("C%03d", n-1)] != 1) {
				t.Errorf("%d countries: wrong counts %v", n, c)
			}
		}
	}
}

// Week asks GoatCounter only for the languages' paths, and refuses an answer
// with any other path, so another code's own count can never be published.
func TestWeekReadsOnlyLanguages(t *testing.T) {
	days, _ := isoweek.Days("2026-W40")
	for _, tt := range []struct {
		hits string
		ok   bool
	}{
		{`[{"path":"/en","stats":[{"day":"2026-09-28","daily":3}]},{"path":"/de","stats":[{"day":"2026-09-28","daily":2}]}]`, true},
		{`[]`, true},
		{`[{"path":"/en","stats":[]},{"path":"/fr","stats":[{"day":"2026-09-28","daily":1}]}]`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch r.URL.Path {
			case "/api/v0/stats/hits":
				if q.Get("path_by_name") != "true" || q.Get("include_paths") != "/en,/de" {
					t.Errorf("hits asked for %v", q)
				}
				fmt.Fprintf(w, `{"hits":%s,"more":false}`, tt.hits)
			default:
				fmt.Fprint(w, `{"stats":[]}`)
			}
		}))
		a := NewAPI(srv.URL, "token")
		a.Pause = 0
		raw, err := a.Week("x.localhost", []string{"en", "de"}, days)
		srv.Close()
		if (err == nil) != tt.ok {
			t.Errorf("%s: err %v", tt.hits, err)
		}
		if tt.ok && raw.Paths["2026-09-28"]["/de"] != strings.Count(tt.hits, `"daily":2`)*2 {
			t.Errorf("%s: paths %v", tt.hits, raw.Paths)
		}
	}
}

func TestWeekRefusesBrokenPaging(t *testing.T) {
	days, _ := isoweek.Days("2026-W40")
	for _, broken := range []string{"repeat", "endless"} {
		if _, err := fakeLocations(t, 150, broken).Week("x.localhost", []string{"en"}, days); err == nil {
			t.Errorf("%s: no error", broken)
		}
	}
}

// While GoatCounter restarts, its port refuses connections for a moment; the
// API keeps trying for RefusedFor.
func TestRetriesRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	a := NewAPI("http://"+addr, "tok")
	a.Pause, a.RefusedFor = 0, 0
	if _, err := a.Version("x.localhost"); err == nil {
		t.Fatal("nothing listening, no retries: no error")
	}

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"v2.7.0"}`)
	})}
	defer srv.Close()
	go func() {
		time.Sleep(time.Second)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Error(err)
			return
		}
		srv.Serve(ln)
	}()
	a.RefusedFor = 10 * time.Second
	start := time.Now()
	v, err := a.Version("x.localhost")
	if err != nil || v != "v2.7.0" {
		t.Fatalf("after a second of refused connections: %q, %v", v, err)
	}
	if took := time.Since(start); took < time.Second || took > 3*time.Second {
		t.Errorf("took %v", took)
	}
}
