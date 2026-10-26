package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tailscale gives every device an IPv4 address from the CGNAT range.
var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

var errNoCLI = errors.New("tailscale command not found")

type tsInfo struct {
	IP   string // this computer's Tailscale IPv4 address
	Name string // its Tailscale machine name, if known
}

type tsNode struct {
	HostName     string
	DNSName      string
	TailscaleIPs []string
}

type tsStatus struct {
	BackendState string
	Self         *tsNode
	Peer         map[string]*tsNode
}

func isTailscaleIP(ip netip.Addr) bool {
	return ip.Is4() && tailscaleRange.Contains(ip)
}

// tailscaleCLI returns the path of the tailscale command-line tool, or "".
func tailscaleCLI() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if d := os.Getenv(env); d != "" {
				candidates = append(candidates, filepath.Join(d, "Tailscale", "tailscale.exe"))
			}
		}
	case "darwin":
		candidates = []string{
			"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
			"/usr/local/bin/tailscale",
			"/opt/homebrew/bin/tailscale",
		}
	default:
		candidates = []string{"/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale", "/snap/bin/tailscale"}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func tailscaleStatus(ctx context.Context) (*tsStatus, error) {
	cli := tailscaleCLI()
	if cli == "" {
		return nil, errNoCLI
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "status", "--json")
	hideWindow(cmd)
	out, err := cmd.Output()
	// "tailscale status" may exit non-zero while logged out but still
	// print the JSON we need, so try to parse whatever came out.
	var st tsStatus
	if jerr := json.Unmarshal(out, &st); jerr != nil || st.BackendState == "" {
		if err == nil {
			err = errors.New("unexpected output from tailscale status")
		}
		return nil, err
	}
	return &st, nil
}

// detectTailscale finds this computer's Tailscale address. When it cannot,
// it returns the translation key of a message that tells the user what to do.
func detectTailscale(ctx context.Context) (tsInfo, string) {
	st, err := tailscaleStatus(ctx)
	if err == nil {
		switch st.BackendState {
		case "Running":
			if st.Self != nil {
				for _, s := range st.Self.TailscaleIPs {
					if ip, err := netip.ParseAddr(s); err == nil && ip.Is4() {
						return tsInfo{IP: s, Name: machineName(st.Self)}, ""
					}
				}
			}
		case "NeedsLogin":
			return tsInfo{}, "ts.needslogin"
		case "Stopped":
			return tsInfo{}, "ts.stopped"
		case "NeedsMachineAuth":
			return tsInfo{}, "ts.needsapproval"
		case "Starting", "NoState":
			return tsInfo{}, "ts.starting"
		}
	}
	// No usable answer from the command-line tool (some installs do not
	// have it): look for an address in the Tailscale range directly.
	if ip := interfaceTailscaleIP(); ip != "" {
		return tsInfo{IP: ip}, ""
	}
	if errors.Is(err, errNoCLI) {
		return tsInfo{}, "ts.notinstalled"
	}
	return tsInfo{}, "ts.notrunning"
}

// waitForTailscale polls until Tailscale is usable, telling the user what is
// missing. It returns false if the user gave up (Ctrl+C).
func waitForTailscale(ctx context.Context, u *ui) (tsInfo, bool) {
	last := ""
	for {
		info, problem := detectTailscale(ctx)
		if problem == "" {
			return info, true
		}
		if problem != last {
			u.warn(u.t(problem))
			last = problem
		}
		select {
		case <-ctx.Done():
			return tsInfo{}, false
		case <-time.After(3 * time.Second):
		}
	}
}

func interfaceTailscaleIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok || !isTailscaleIP(ip.Unmap()) {
				continue
			}
			name := strings.ToLower(ifc.Name)
			if strings.Contains(name, "tailscale") || strings.HasPrefix(name, "utun") {
				return ip.Unmap().String()
			}
			if fallback == "" {
				fallback = ip.Unmap().String()
			}
		}
	}
	return fallback
}

// machineName is the first label of the MagicDNS name ("office-pc" for
// "office-pc.tail1234.ts.net."), which is what the Tailscale apps show.
func machineName(n *tsNode) string {
	if n == nil {
		return ""
	}
	if label, _, _ := strings.Cut(n.DNSName, "."); label != "" {
		return label
	}
	return n.HostName
}

// peerNames maps the Tailscale addresses of connecting computers to their
// machine names for the activity log.
type peerNames struct {
	mu      sync.Mutex
	names   map[string]string
	fetched time.Time
	fetch   func() map[string]string
}

func newPeerNames() *peerNames {
	return &peerNames{names: map[string]string{}, fetch: fetchPeerNames}
}

func (p *peerNames) lookup(ip string) string {
	if ip == "127.0.0.1" || ip == "::1" {
		return "localhost"
	}
	p.mu.Lock()
	if n, ok := p.names[ip]; ok {
		p.mu.Unlock()
		return n
	}
	stale := p.fetch != nil && time.Since(p.fetched) > 30*time.Second
	if stale {
		p.fetched = time.Now()
	}
	p.mu.Unlock()
	if !stale {
		return ip
	}
	fresh := p.fetch()
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range fresh {
		p.names[k] = v
	}
	if n, ok := p.names[ip]; ok {
		return n
	}
	return ip
}

func fetchPeerNames() map[string]string {
	st, err := tailscaleStatus(context.Background())
	if err != nil {
		return nil
	}
	m := map[string]string{}
	add := func(n *tsNode) {
		if name := machineName(n); name != "" {
			for _, ip := range n.TailscaleIPs {
				m[ip] = name
			}
		}
	}
	if st.Self != nil {
		add(st.Self)
	}
	for _, n := range st.Peer {
		add(n)
	}
	return m
}
