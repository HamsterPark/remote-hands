// Command remote-hands lets an AI coding agent (Claude Code, Codex, ...) on
// one computer control another computer through Tailscale.
//
// Run it on the computer that should be controlled. It serves the Model
// Context Protocol (streamable HTTP) on that computer's Tailscale address
// and prints two lines that the user pastes into the agent on the other
// computer.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

const defaultPort = 47863

type options struct {
	lang       string
	name       string
	port       int
	newKey     bool
	listen     string
	allowSleep bool
	version    bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func appVersion() string {
	if version != "dev" {
		return strings.TrimPrefix(version, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return version
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("remote-hands", flag.ContinueOnError)
	fs.StringVar(&o.lang, "lang", "", "language of this window: en, zh or cs (default: the system language)")
	fs.StringVar(&o.name, "name", "", "name the AI should use for this computer (default: its Tailscale name); remembered")
	fs.IntVar(&o.port, "port", 0, fmt.Sprintf("TCP port to listen on (default %d); remembered", defaultPort))
	fs.BoolVar(&o.newKey, "new-key", false, "make a new secret key, so that the old connection lines stop working")
	fs.StringVar(&o.listen, "listen", "", "for testing only: listen on this host:port instead of the Tailscale address")
	fs.BoolVar(&o.allowSleep, "allow-sleep", false, "do not keep this computer awake while Remote Hands runs")
	fs.BoolVar(&o.version, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Remote Hands %s - let Claude Code or Codex on another computer control this one over Tailscale.\n\n", appVersion())
		fmt.Fprintf(fs.Output(), "Usage: remote-hands [options]\n\nOptions:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nGuide: https://github.com/HamsterPark/remote-hands\n")
	}
	err := fs.Parse(args)
	if err == nil && fs.NArg() > 0 {
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
		fmt.Fprintln(fs.Output(), err)
		fs.Usage()
	}
	return o, err
}

func run(args []string) int {
	opts, err := parseFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if opts.version {
		fmt.Println("remote-hands", appVersion())
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	u := newUI(os.Stdout, newTranslator(pickLanguage(opts.lang)), setupConsole())
	setConsoleTitle("Remote Hands")
	u.banner(appVersion())

	cfgPath, err := configPath()
	if err != nil {
		return u.fatal(ctx, err.Error())
	}
	cfg := loadConfig(cfgPath)
	savedPort := cfg.Port
	if opts.newKey || !validToken(cfg.Token) {
		cfg.Token = newToken()
	}
	if opts.port > 0 {
		cfg.Port = opts.port
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = defaultPort
	}
	if opts.name != "" {
		cfg.Name = sanitizeName(opts.name)
	}

	host, tsName, exactPort := "", "", false
	if opts.listen != "" {
		h, p, err := net.SplitHostPort(opts.listen)
		port, perr := strconv.Atoi(p)
		if err != nil || perr != nil || port <= 0 || port > 65535 {
			return u.fatal(ctx, fmt.Sprintf("--listen needs host:port, for example 127.0.0.1:%d", defaultPort))
		}
		host, cfg.Port, exactPort = h, port, true
		u.warn(u.t("listen.dev"))
	} else {
		ts, ok := waitForTailscale(ctx, u)
		if !ok {
			u.say(u.t("stopped"))
			return 0
		}
		host, tsName = ts.IP, ts.Name
	}

	name := cfg.Name
	if name == "" {
		name = sanitizeName(firstNonEmpty(tsName, hostname()))
	}
	urlHost := host
	if urlHost == "" || urlHost == "0.0.0.0" || urlHost == "::" {
		urlHost = "127.0.0.1"
	}
	if opts.listen == "" {
		u.ok(u.t("ts.ok", name, host))
	}

	// Started a second time (for example to see the two lines again):
	// show the lines of the copy that is already running. Changing its key,
	// name or port has to happen in that copy, or a "revoked" key would
	// silently keep working there.
	if !exactPort && (alreadyRunning(urlHost, cfg.Port) || savedPort != cfg.Port && alreadyRunning(urlHost, savedPort)) {
		if opts.newKey || opts.name != "" || opts.port > 0 {
			return u.fatal(ctx, u.t("already.flags"))
		}
		cur := loadConfig(cfgPath)
		if validToken(cur.Token) && cur.Port == cfg.Port {
			u.say(u.t("already"))
			url := mcpURL(urlHost, cur.Port, cur.Token)
			lines := connectionLines(name, url)
			u.showLines(lines, copyToClipboard(joinLines(lines)))
			u.pause(ctx)
			return 0
		}
	}

	ln, err := listenOn(host, cfg.Port, exactPort)
	if err != nil {
		return u.fatal(ctx, u.t("listen.fail", err))
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	toSave := *cfg
	if exactPort {
		toSave.Port = savedPort // --listen is for tests; keep the normal port
	}
	if err := saveConfig(cfgPath, &toSave); err != nil {
		u.warn(u.t("config.fail", cfgPath, err))
	}

	a := newApp(name, urlHost, cfg.Token, u)
	a.tailnetOnly = !exactPort
	srv := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	// shutdown stops accepting requests first, so that no command can start
	// while the running ones are being stopped.
	shutdown := func() {
		_ = srv.Close()
		a.jobs.killAll()
	}
	// --listen is for tests: leave the developer's clipboard alone.
	copyLines := func(lines [2]string) bool { return !exactPort && copyToClipboard(joinLines(lines)) }

	awake := false
	if !opts.allowSleep {
		var release func()
		release, awake = keepAwake()
		defer release()
	}

	setConsoleTitle("Remote Hands - " + name)
	if exactPort {
		u.ok(u.t("listening.test", ln.Addr().String()))
	} else {
		u.ok(u.t("listening", ln.Addr().String()))
	}
	url := mcpURL(urlHost, cfg.Port, cfg.Token)
	lines := connectionLines(name, url)
	u.showLines(lines, copyLines(lines))
	interactive := isTerminal(os.Stdin)
	u.tips(awake, interactive)
	u.startActivityLog()

	var commands chan string
	if interactive {
		commands = make(chan string)
		go readCommands(os.Stdin, commands)
	}
	for {
		select {
		case <-ctx.Done():
			shutdown()
			u.say("\n" + u.t("stopped"))
			return 0
		case err := <-serveErr:
			shutdown()
			return u.fatal(ctx, u.t("listen.fail", err))
		case line, ok := <-commands:
			if !ok {
				commands = nil // stdin closed; keep serving
				continue
			}
			switch normalizeCommand(line) {
			case "new":
				cfg.Token = newToken()
				a.setToken(cfg.Token)
				toSave.Token = cfg.Token
				if err := saveConfig(cfgPath, &toSave); err != nil {
					u.warn(u.t("config.fail", cfgPath, err))
				}
				url = mcpURL(urlHost, cfg.Port, cfg.Token)
				lines = connectionLines(name, url)
				u.say("\n" + u.t("newkey.done"))
				u.showLines(lines, copyLines(lines))
			case "quit":
				shutdown()
				u.say(u.t("stopped"))
				return 0
			}
		}
	}
}

// connectionLines returns the two lines the user pastes into the AI agent.
// They are plain ASCII without quotes that chat apps could "smarten", and
// they are written for the agent, which knows how to register MCP servers.
func connectionLines(name, url string) [2]string {
	return [2]string{
		fmt.Sprintf("Remote Hands: please connect to my computer %s. It is an MCP server (streamable HTTP; the key is inside the URL, no other login): %s", name, url),
		fmt.Sprintf("How: Claude Code -> claude mcp add -s user -t http %s <URL> (if %s already exists, first: claude mcp remove -s user %s) ; Codex -> codex mcp add %s --url <URL> ; then tell me to restart you, and use the %s tools (start with system_info).",
			name, name, name, name, name),
	}
}

func joinLines(lines [2]string) string {
	nl := "\n"
	if runtime.GOOS == "windows" {
		nl = "\r\n"
	}
	return lines[0] + nl + lines[1]
}

func mcpURL(host string, port int, token string) string {
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/mcp/" + token
}

// listenOn listens on host:port. Unless exact is set it tries the next
// ports too, because some other program may already use the preferred one.
func listenOn(host string, port int, exact bool) (net.Listener, error) {
	tries := 20
	if exact {
		tries = 1
	}
	var first error
	for i := 0; i < tries && port+i <= 65535; i++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port+i)))
		if err == nil {
			return ln, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}

// alreadyRunning reports whether another Remote Hands answers on host:port.
func alreadyRunning(host string, port int) bool {
	client := &http.Client{
		Timeout:   1500 * time.Millisecond,
		Transport: &http.Transport{Proxy: nil}, // never send Tailscale addresses to a proxy
	}
	resp, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return resp.StatusCode == http.StatusOK && strings.HasPrefix(string(b), "remote-hands")
}

func readCommands(r io.Reader, out chan<- string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		out <- sc.Text()
	}
	close(out)
}

// normalizeCommand maps what the user typed in the window to "new", "quit"
// or "".
func normalizeCommand(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "new", "n", "new key", "新", "新密钥", "nový", "novy", "nový klíč":
		return "new"
	case "quit", "q", "exit", "stop", "退出", "konec":
		return "quit"
	}
	return ""
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// plainLetters drops accents from the letters of Czech and other European
// names, so "Kuchyňský počítač" becomes "kuchynsky-pocitac".
var plainLetters = strings.NewReplacer(
	"á", "a", "ä", "a", "à", "a", "â", "a", "č", "c", "ç", "c", "ď", "d", "é", "e", "ě", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "î", "i", "ï", "i", "ľ", "l", "ĺ", "l", "ł", "l", "ň", "n", "ñ", "n", "ó", "o", "ö", "o", "ô", "o", "ő", "o",
	"ř", "r", "ŕ", "r", "š", "s", "ś", "s", "ß", "ss", "ť", "t", "ú", "u", "ů", "u", "ü", "u", "ű", "u", "ý", "y", "ž", "z", "ź", "z", "ż", "z",
)

// sanitizeName turns a computer name into something usable as an MCP
// server name: lower-case letters, digits and dashes.
func sanitizeName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range plainLetters.Replace(strings.ToLower(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "remote-computer"
	}
	return out
}
