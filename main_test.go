package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"DESKTOP-7GH2K9L":       "desktop-7gh2k9l",
		"Alice's MacBook Air":   "alice-s-macbook-air",
		"  --office pc--  ":     "office-pc",
		"张三的电脑":                 "remote-computer",
		"Kuchyňský počítač":     "kuchynsky-pocitac",
		"ŘEDITELNA":             "reditelna",
		strings.Repeat("a", 60): strings.Repeat("a", 40),
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConnectionLines(t *testing.T) {
	url := mcpURL("100.86.1.2", 47863, testToken)
	if url != "http://100.86.1.2:47863/mcp/"+testToken {
		t.Fatalf("url = %s", url)
	}
	lines := connectionLines("office-pc", url)
	if !strings.Contains(lines[0], url) || !strings.Contains(lines[0], "office-pc") {
		t.Errorf("line 1 lacks URL or name: %s", lines[0])
	}
	for _, want := range []string{"claude mcp add -s user -t http office-pc", "codex mcp add office-pc --url", "restart"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("line 2 lacks %q: %s", want, lines[1])
		}
	}
	// The lines travel through e-mail and chat apps: keep them plain ASCII
	// and free of quotes that could be turned into curly quotes.
	for i, l := range lines {
		for _, r := range l {
			if r > 126 || r == '"' || r == '\'' || r == '`' {
				t.Errorf("line %d contains %q", i+1, r)
			}
		}
	}
}

func TestNormalizeCommand(t *testing.T) {
	for in, want := range map[string]string{"new": "new", " NEW ": "new", "nový": "new", "新": "new", "quit": "quit", "hello": ""} {
		if got := normalizeCommand(in); got != want {
			t.Errorf("normalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "config.json")
	if c := loadConfig(p); c.Token != "" || c.Port != 0 {
		t.Errorf("missing file gave %+v", c)
	}
	tok := newToken()
	if !validToken(tok) || validToken("xyz") || validToken(strings.Repeat("g", 32)) {
		t.Error("validToken is wrong")
	}
	if err := saveConfig(p, &config{Token: tok, Port: 47864, Name: "office-pc"}); err != nil {
		t.Fatal(err)
	}
	if c := loadConfig(p); c.Token != tok || c.Port != 47864 || c.Name != "office-pc" {
		t.Errorf("loaded %+v", c)
	}
}

var verb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]`)

func TestTranslationsComplete(t *testing.T) {
	for lang, msgs := range messages {
		for key, en := range messages["en"] {
			s, ok := msgs[key]
			if !ok {
				t.Errorf("%s: missing %q", lang, key)
				continue
			}
			if a, b := verb.FindAllString(en, -1), verb.FindAllString(s, -1); strings.Join(a, " ") != strings.Join(b, " ") {
				t.Errorf("%s %q: format verbs %v, English has %v", lang, key, b, a)
			}
		}
		for key := range msgs {
			if _, ok := messages["en"][key]; !ok {
				t.Errorf("%s: extra key %q", lang, key)
			}
		}
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"zh": "zh", "zh_CN.UTF-8": "zh", "zh-Hans-CN": "zh", "cs": "cs", "cs_CZ.UTF-8": "cs",
		"cs-CZ": "cs", "CZ": "cs", "en_US.UTF-8": "en", "de_DE.UTF-8": "", "": "", "csb_PL": "",
	}
	for in, want := range cases {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}
