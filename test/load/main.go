// Command load sends counts to nginx the way browsers do, for
// test/load/run.sh: a steady rate from many different public IP addresses,
// then a burst. It prints one JSON line per phase, and exits with status 1
// unless every request got an empty 204. Test use only.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"
)

type result struct {
	Phase     string         `json:"phase"`
	Start     int64          `json:"start"`
	End       int64          `json:"end"`
	Sent      int            `json:"sent"`
	Empty204  int            `json:"empty_204"`
	Other     map[string]int `json:"other,omitempty"`
	P50ms     float64        `json:"p50_ms"`
	P99ms     float64        `json:"p99_ms"`
	MaxMs     float64        `json:"max_ms"`
	PerSecond float64        `json:"per_second"`
}

// publicIP is a random IPv4 address outside the private, reserved and
// documentation ranges, so GoatCounter uses it and looks up its country.
func publicIP() string {
	for {
		a, b := rand.N(224), rand.N(256)
		switch {
		case a == 0, a == 10, a == 127, a == 100 && b >= 64 && b < 128,
			a == 169 && b == 254, a == 172 && b >= 16 && b < 32, a == 192 && b == 168,
			a == 192 && b == 0, a == 198 && (b == 18 || b == 19 || b == 51), a == 203 && b == 0:
			continue
		}
		return fmt.Sprintf("%d.%d.%d.%d", a, b, rand.N(256), 1+rand.N(254))
	}
}

func main() {
	addr := flag.String("addr", "vps:443", "nginx address")
	rate := flag.Float64("rate", 20, "requests per second in the steady phase")
	dur := flag.Duration("duration", 150*time.Second, "length of the steady phase")
	burst := flag.Int("burst", 200, "requests sent at once after the steady phase")
	phase := flag.String("phase", "steady", "name of the steady phase in the output")
	flag.Parse()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// Every visitor opens its own connection.
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{ServerName: "stats.irq.dk", InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, *addr)
			},
		},
	}
	one := func(rec func(time.Duration, string)) {
		lang := "en"
		if rand.N(4) == 0 {
			lang = "da"
		}
		req, _ := http.NewRequest("GET", "https://stats.irq.dk/how-the-internet-works/count?lang="+lang, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Origin", "https://tkjaer.github.io")
		req.Header.Set("X-Test-Client-IP", publicIP())
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			rec(time.Since(start), "error")
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 204 && len(body) == 0 {
			rec(time.Since(start), "")
		} else {
			rec(time.Since(start), fmt.Sprintf("status %d, %d bytes", resp.StatusCode, len(body)))
		}
	}
	failed := false
	run := func(name string, send func(go1 func())) {
		res := result{Phase: name, Other: map[string]int{}, Start: time.Now().Unix()}
		var mu sync.Mutex
		var lat []float64
		var wg sync.WaitGroup
		t0 := time.Now()
		send(func() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				one(func(d time.Duration, bad string) {
					mu.Lock()
					defer mu.Unlock()
					res.Sent++
					lat = append(lat, float64(d.Microseconds())/1000)
					if bad == "" {
						res.Empty204++
					} else {
						res.Other[bad]++
					}
				})
			}()
		})
		wg.Wait()
		res.End = time.Now().Unix()
		res.PerSecond = float64(res.Sent) / time.Since(t0).Seconds()
		slices.Sort(lat)
		if len(lat) > 0 {
			res.P50ms, res.P99ms, res.MaxMs = lat[len(lat)/2], lat[len(lat)*99/100], lat[len(lat)-1]
		}
		json.NewEncoder(os.Stdout).Encode(res)
		if res.Empty204 != res.Sent {
			failed = true
		}
	}

	run(*phase, func(go1 func()) {
		tick := time.NewTicker(time.Duration(float64(time.Second) / *rate))
		defer tick.Stop()
		end := time.Now().Add(*dur)
		for time.Now().Before(end) {
			<-tick.C
			go1()
		}
	})
	if *burst > 0 {
		run("burst", func(go1 func()) {
			for range *burst {
				go1()
			}
		})
	}
	// Every request must get an empty 204, also when nginx's cap rejects it;
	// anything else, including a request that failed, is a failure.
	if failed {
		fmt.Fprintln(os.Stderr, "load: not every request got an empty 204")
		os.Exit(1)
	}
}
