package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const disabledDelay = time.Hour

var (
	stats     sync.Map
	startTime = time.Now()
)

type status struct {
	id            string
	ip            string
	host          string
	fcm           atomic.Uintptr
	apn           atomic.Uintptr
	forwarded     atomic.Uintptr
	disabledUntil atomic.Pointer[time.Time]
}

func (s *status) isDisabled() bool {
	t := s.disabledUntil.Load()
	if t != nil {
		if time.Now().Before(*t) {
			return true
		} else {
			return !s.disabledUntil.CompareAndSwap(t, nil)
		}
	}
	return false
}

func (s *status) disable() {
	t := time.Now().Add(disabledDelay)
	s.disabledUntil.Store(&t)
}

func getStats(id, ip, host string) *status {
	key := id + ip + host
	stat, ok := stats.Load(key)
	if !ok {
		s := status{
			id:   id,
			ip:   ip,
			host: host,
		}
		stat, _ = stats.LoadOrStore(key, &s)
	}
	return stat.(*status)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("StatsHandler for %s from %s", r.RequestURI, getIP(r))
	out := `
<!DOCTYPE html>
<html><head>
<title>Rocket.Chat Push Gateway Stats</title>
<style>
	body {
		font-family: sans-serif;
	}
	table, th, td {
		border: 1px solid #ddd;
		border-collapse: collapse;
		padding: 2px 6px;
	}
</style>
</head><body>
<h2>Rocket.Chat Push Gateway Stats</h2>`
	out += fmt.Sprintf("<p>Uptime: %s</p>", time.Since(startTime).Truncate(time.Second))
	out += `<table><thead><tr>
<th>id</th><th>ip</th><th>host</th><th>direct</th><th>apn</th><th>fcm</th><th>forwards</th>
</tr></thead><tbody>
`
	stats.Range(func(_, v any) bool {
		stats := v.(*status)
		apn := stats.apn.Load()
		fcm := stats.fcm.Load()
		forwarded := stats.forwarded.Load()
		out += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%d</td><td>%d</td></tr>",
			stats.id, stats.ip, stats.host, apn+fcm-forwarded, apn, fcm, forwarded)
		return true
	})
	out += "</tbody></table></body></html>"
	io.WriteString(w, out)
}
