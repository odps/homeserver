package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Recogida de datos para el panel. Todo es de solo lectura salvo restart. Lo
// que tarda (termux-api) se guarda en caché un minuto.

type appStatus struct {
	Name    string
	Port    int
	OK      bool
	Version string
	Error   string
}

func (g *gateway) appStatuses(ctx context.Context) []appStatus {
	names := make([]string, 0, len(g.apps))
	for n := range g.apps {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]appStatus, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := g.apps[n]
			st := appStatus{Name: n, Port: a.Port}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/health", a.Port), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				st.Error = "no responde"
			} else {
				var h struct{ Version, DB string }
				json.NewDecoder(resp.Body).Decode(&h)
				resp.Body.Close()
				st.Version, st.OK = h.Version, resp.StatusCode == http.StatusOK
				if !st.OK {
					st.Error = "base de datos: " + h.DB
				}
			}
			out[i] = st
		}()
	}
	wg.Wait()
	return out
}

type serviceStatus struct {
	Name   string
	Up     bool
	State  string // run, down, finish
	Uptime string
}

var svLine = regexp.MustCompile(`^(\w+): ([^:]+): (?:\(pid \d+\) )?(\d+)s`)

// services lee el estado de todos los servicios runit con un solo «sv status».
func (g *gateway) services(ctx context.Context) []serviceStatus {
	entries, _ := os.ReadDir(g.svDir)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(g.svDir, e.Name()))
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	out, _ := exec.CommandContext(ctx, "sv", append([]string{"status"}, dirs...)...).Output()
	var list []serviceStatus
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		m := svLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := filepath.Base(m[2])
		secs, _ := strconv.Atoi(m[3])
		// Los servicios con fichero «down» y parados son los desactivados a propósito.
		list = append(list, serviceStatus{Name: name, Up: m[1] == "run", State: m[1], Uptime: humanDuration(time.Duration(secs) * time.Second)})
	}
	return list
}

func (g *gateway) restartService(ctx context.Context, name string) error {
	if !validTokenName(name) {
		return fmt.Errorf("servicio no válido")
	}
	dir := filepath.Join(g.svDir, name)
	if _, err := os.Stat(filepath.Join(dir, "run")); err != nil {
		return fmt.Errorf("no existe el servicio %s", name)
	}
	if name == "gateway" {
		// Reiniciarse a sí misma cortaría la respuesta: lo hace después.
		go func() {
			time.Sleep(time.Second)
			exec.Command("sv", "restart", dir).Run()
		}()
		return nil
	}
	out, err := exec.CommandContext(ctx, "sv", "restart", dir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

type phoneStatus struct {
	HasBattery    bool // termux-api respondió
	HasWifi       bool
	BatteryPct    int
	BatteryTemp   float64
	BatteryStatus string
	Plugged       bool
	CPUTemp       float64
	MemUsedPct    int
	MemAvailMB    int
	DiskUsedPct   int
	DiskFreeGB    float64
	WifiRSSI      int
	WifiMHz       int
	WifiMbps      int
	WifiIP        string
	Updated       time.Time
}

type cached[T any] struct {
	mu   sync.Mutex
	val  T
	when time.Time
}

func (c *cached[T]) get(ttl time.Duration, fill func() T) T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.when) > ttl {
		c.val, c.when = fill(), time.Now()
	}
	return c.val
}

func (g *gateway) phone(ctx context.Context) phoneStatus {
	slow := g.slowPhone.get(time.Minute, func() phoneStatus {
		var p phoneStatus
		var bat struct {
			Percentage  int     `json:"percentage"`
			Temperature float64 `json:"temperature"`
			Status      string  `json:"status"`
			Plugged     string  `json:"plugged"`
		}
		if runJSON(ctx, &bat, "termux-battery-status") == nil {
			p.HasBattery = true
			p.BatteryPct, p.BatteryTemp, p.BatteryStatus = bat.Percentage, bat.Temperature, batteryStatus(bat.Status)
			p.Plugged = bat.Plugged != "" && bat.Plugged != "UNPLUGGED"
		}
		var wifi struct {
			RSSI      int    `json:"rssi"`
			Frequency int    `json:"frequency_mhz"`
			Link      int    `json:"link_speed_mbps"`
			IP        string `json:"ip"`
		}
		if runJSON(ctx, &wifi, "termux-wifi-connectioninfo") == nil {
			p.HasWifi = wifi.IP != ""
			p.WifiRSSI, p.WifiMHz, p.WifiMbps, p.WifiIP = wifi.RSSI, wifi.Frequency, wifi.Link, wifi.IP
		}
		return p
	})
	p := slow
	p.CPUTemp = maxCPUTemp()
	if total, avail := meminfo(); total > 0 {
		p.MemUsedPct = int(100 - avail*100/total)
		p.MemAvailMB = int(avail / 1024)
	}
	p.DiskUsedPct, p.DiskFreeGB = diskUsage(g.srvDir)
	p.Updated = time.Now()
	return p
}

func runJSON(ctx context.Context, v any, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name).Output()
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

func batteryStatus(s string) string {
	switch s {
	case "CHARGING":
		return "cargando"
	case "DISCHARGING":
		return "descargando"
	case "FULL":
		return "llena"
	case "NOT_CHARGING":
		return "sin cargar (límite)"
	}
	return strings.ToLower(s)
}

func maxCPUTemp() float64 {
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	best := 0.0
	for _, z := range zones {
		t, err := os.ReadFile(filepath.Join(z, "type"))
		if err != nil || !strings.HasPrefix(string(t), "cpu") {
			continue
		}
		v, err := os.ReadFile(filepath.Join(z, "temp"))
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(v))); err == nil && float64(n)/1000 > best {
			best = float64(n) / 1000
		}
	}
	return best
}

func meminfo() (totalKB, availKB uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			totalKB = v
		case "MemAvailable:":
			availKB = v
		}
	}
	return
}

type netEvent struct {
	Time   time.Time      `json:"time"`
	Event  string         `json:"event"`
	Detail map[string]any `json:"detail"`
}

type deployEvent struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	Tag    string    `json:"tag"`
	Prev   string    `json:"prev,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

type fileInfo struct {
	Name string
	Size string
	Time time.Time
}

type deployStatus struct {
	Current  string
	Releases []string
	Bad      []string
	Backups  []fileInfo
	Events   []deployEvent
}

func (g *gateway) deploys() deployStatus {
	var d deployStatus
	if target, err := os.Readlink(filepath.Join(g.srvDir, "current")); err == nil {
		d.Current = filepath.Base(target)
	}
	entries, _ := os.ReadDir(filepath.Join(g.srvDir, "releases"))
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			d.Releases = append(d.Releases, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(d.Releases)))
	if b, err := os.ReadFile(filepath.Join(g.srvDir, "state", "bad")); err == nil {
		d.Bad = strings.Fields(string(b))
	}
	dumps, _ := filepath.Glob(filepath.Join(g.srvDir, "backups", "*.dump"))
	sort.Sort(sort.Reverse(sort.StringSlice(dumps)))
	for _, p := range dumps {
		if st, err := os.Stat(p); err == nil {
			d.Backups = append(d.Backups, fileInfo{Name: filepath.Base(p), Size: humanBytes(st.Size()), Time: st.ModTime()})
		}
	}
	d.Events = tailJSONL[deployEvent](filepath.Join(g.srvDir, "state", "deploys.jsonl"), 10)
	return d
}

func (g *gateway) netEvents() []netEvent {
	return tailJSONL[netEvent](filepath.Join(g.srvDir, "state", "netwatch.jsonl"), 5)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

func humanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
