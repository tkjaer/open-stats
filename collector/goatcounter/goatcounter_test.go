// SPDX-License-Identifier: AGPL-3.0-or-later

package goatcounter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
		raw, err := fakeLocations(t, n, "").Week("x.localhost", days)
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

func TestWeekRefusesBrokenPaging(t *testing.T) {
	days, _ := isoweek.Days("2026-W40")
	for _, broken := range []string{"repeat", "endless"} {
		if _, err := fakeLocations(t, 150, broken).Week("x.localhost", days); err == nil {
			t.Errorf("%s: no error", broken)
		}
	}
}
