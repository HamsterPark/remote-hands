package main

import (
	"fmt"
	"os"
	"strings"
)

// Messages shown in the window on the controlled computer. Everything the
// AI sees (tool descriptions and results) stays in English.
var messages = map[string]map[string]string{
	"en": {
		"tagline":          "Lets Claude Code or Codex on your other computer control THIS computer\nthrough your private Tailscale network.",
		"ts.notinstalled":  "Tailscale was not found on this computer. Please install Tailscale and log in (see the guide). This window will continue by itself.",
		"ts.notrunning":    "Tailscale is installed but not running. Please open the Tailscale app. This window will continue by itself.",
		"ts.needslogin":    "Tailscale is not logged in. Please open Tailscale and log in. This window will continue by itself.",
		"ts.stopped":       "Tailscale is switched off. Please open Tailscale and connect it. This window will continue by itself.",
		"ts.needsapproval": "This computer is waiting for approval in your Tailscale admin console. This window will continue by itself.",
		"ts.starting":      "Tailscale is starting, please wait...",
		"ts.ok":            "Tailscale is connected. This computer is called %s (%s).",
		"listening":        "Waiting for your AI on %s (only reachable through Tailscale).",
		"listening.test":   "Waiting for your AI on %s.",
		"listen.fail":      "Could not start: %v",
		"listen.dev":       "WARNING: --listen is only for testing. Other people may be able to reach this address.",
		"already":          "Remote Hands is already running in another window on this computer. Here are its 2 lines again:",
		"config.fail":      "Could not save the settings file %s: %v",
		"lines.title":      "COPY THE 2 LINES BELOW and paste them into Claude Code or Codex on your OTHER computer:",
		"lines.copied":     "(They are already copied to this computer's clipboard. To get them to your other computer, e-mail them to yourself or send them to yourself in a chat app.)",
		"lines.nocopy":     "(Select both lines with the mouse and copy them. To get them to your other computer, e-mail them to yourself or send them to yourself in a chat app.)",
		"tips.open":        "Keep this window open. Close it to stop the remote control.",
		"tips.awake":       "This computer will not fall asleep while this window is open.",
		"tips.secret":      "Anyone who has these 2 lines and is in your Tailscale network can fully use this computer. Do not share them.",
		"tips.newkey":      "To make the old 2 lines stop working, type  new  and press Enter.",
		"activity":         "What the AI does on this computer:",
		"newkey.done":      "New key made. The old 2 lines do not work any more. Here are the new ones:",
		"stopped":          "Stopped. Nobody can control this computer through Remote Hands now.",
		"press.enter":      "Press Enter to close this window.",
		"act.connected":    "connected (%s)",
		"act.badkey":       "tried to connect with a wrong or old key",
		"act.info":         "looked at the system information",
		"act.run":          "ran a command: %s",
		"act.check":        "checked on command #%d",
		"act.stop":         "stopped command #%d",
		"act.read":         "read the file %s",
		"act.write":        "wrote the file %s",
		"act.edit":         "changed the file %s",
		"act.list":         "looked into the folder %s",
		"act.screenshot":   "took a screenshot",
	},
	"zh": {
		"tagline":          "让你另一台电脑上的 Claude Code 或 Codex 通过你自己的 Tailscale 私人网络\n来控制【这台】电脑。",
		"ts.notinstalled":  "这台电脑上没有找到 Tailscale。请先安装 Tailscale 并登录（见使用说明）。之后这个窗口会自动继续。",
		"ts.notrunning":    "Tailscale 已安装，但没有在运行。请打开 Tailscale 应用。之后这个窗口会自动继续。",
		"ts.needslogin":    "Tailscale 还没有登录。请打开 Tailscale 并登录。之后这个窗口会自动继续。",
		"ts.stopped":       "Tailscale 现在是断开状态。请打开 Tailscale 并连接（Connect）。之后这个窗口会自动继续。",
		"ts.needsapproval": "这台电脑正在等待你在 Tailscale 管理后台里批准。之后这个窗口会自动继续。",
		"ts.starting":      "Tailscale 正在启动，请稍候……",
		"ts.ok":            "Tailscale 已连接。这台电脑的名字是 %s（%s）。",
		"listening":        "正在 %s 等待你的 AI 连接（只能通过 Tailscale 访问）。",
		"listening.test":   "正在 %s 等待你的 AI 连接。",
		"listen.fail":      "无法启动：%v",
		"listen.dev":       "警告：--listen 只用于测试，别人可能也能访问这个地址。",
		"already":          "Remote Hands 已经在这台电脑的另一个窗口里运行了。下面再显示一次它的两行连接信息：",
		"config.fail":      "无法保存设置文件 %s：%v",
		"lines.title":      "请复制下面这两行，粘贴到你【另一台电脑】上的 Claude Code 或 Codex 里：",
		"lines.copied":     "（这两行已经复制到这台电脑的剪贴板里了。要把它们传到另一台电脑，可以发邮件给自己，或者用微信“文件传输助手”等聊天工具发给自己。）",
		"lines.nocopy":     "（请用鼠标选中这两行并复制。要把它们传到另一台电脑，可以发邮件给自己，或者用微信“文件传输助手”等聊天工具发给自己。）",
		"tips.open":        "请让这个窗口保持打开。关掉这个窗口，远程控制就停止了。",
		"tips.awake":       "这个窗口开着的时候，这台电脑不会自动睡眠。",
		"tips.secret":      "任何拿到这两行、并且在你的 Tailscale 网络里的人，都能完全使用这台电脑。不要把它们给别人。",
		"tips.newkey":      "想让旧的两行失效：输入  new  然后按回车。",
		"activity":         "AI 在这台电脑上做了什么：",
		"newkey.done":      "已生成新密钥，旧的两行已经失效。新的两行如下：",
		"stopped":          "已停止。现在没有人能通过 Remote Hands 控制这台电脑了。",
		"press.enter":      "按回车键关闭这个窗口。",
		"act.connected":    "已连接（%s）",
		"act.badkey":       "用错误或过期的密钥尝试连接",
		"act.info":         "查看了系统信息",
		"act.run":          "执行了命令：%s",
		"act.check":        "查看了命令 #%d 的进度",
		"act.stop":         "停止了命令 #%d",
		"act.read":         "读取了文件 %s",
		"act.write":        "写入了文件 %s",
		"act.edit":         "修改了文件 %s",
		"act.list":         "查看了文件夹 %s",
		"act.screenshot":   "截取了屏幕画面",
	},
	"cs": {
		"tagline":          "Umožní Claude Code nebo Codexu na vašem druhém počítači ovládat TENTO počítač\npřes vaši soukromou síť Tailscale.",
		"ts.notinstalled":  "Na tomto počítači nebyl nalezen Tailscale. Nainstalujte Tailscale a přihlaste se (viz návod). Toto okno pak bude pokračovat samo.",
		"ts.notrunning":    "Tailscale je nainstalovaný, ale neběží. Otevřete aplikaci Tailscale. Toto okno pak bude pokračovat samo.",
		"ts.needslogin":    "Tailscale není přihlášený. Otevřete Tailscale a přihlaste se. Toto okno pak bude pokračovat samo.",
		"ts.stopped":       "Tailscale je vypnutý. Otevřete Tailscale a připojte ho (Connect). Toto okno pak bude pokračovat samo.",
		"ts.needsapproval": "Tento počítač čeká na schválení v administraci Tailscale. Toto okno pak bude pokračovat samo.",
		"ts.starting":      "Tailscale se spouští, chvíli strpení…",
		"ts.ok":            "Tailscale je připojený. Tento počítač se jmenuje %s (%s).",
		"listening":        "Čekám na vaši AI na adrese %s (dostupné jen přes Tailscale).",
		"listening.test":   "Čekám na vaši AI na adrese %s.",
		"listen.fail":      "Nepodařilo se spustit: %v",
		"listen.dev":       "UPOZORNĚNÍ: --listen slouží jen k testování. Tuto adresu mohou vidět i jiní lidé.",
		"already":          "Remote Hands už na tomto počítači běží v jiném okně. Tady jsou znovu jeho 2 řádky:",
		"config.fail":      "Nepodařilo se uložit soubor s nastavením %s: %v",
		"lines.title":      "ZKOPÍRUJTE 2 ŘÁDKY NÍŽE a vložte je do Claude Code nebo Codexu na svém DRUHÉM počítači:",
		"lines.copied":     "(Už jsou zkopírované ve schránce tohoto počítače. Na druhý počítač je dostanete tak, že si je pošlete e-mailem nebo zprávou v chatovací aplikaci.)",
		"lines.nocopy":     "(Označte myší oba řádky a zkopírujte je. Na druhý počítač je dostanete tak, že si je pošlete e-mailem nebo zprávou v chatovací aplikaci.)",
		"tips.open":        "Nechte toto okno otevřené. Zavřením okna vzdálené ovládání ukončíte.",
		"tips.awake":       "Dokud je toto okno otevřené, počítač neusne.",
		"tips.secret":      "Kdo má tyto 2 řádky a je ve vaší síti Tailscale, může tento počítač plně používat. Nikomu je nedávejte.",
		"tips.newkey":      "Aby staré 2 řádky přestaly fungovat, napište  new  a stiskněte Enter.",
		"activity":         "Co AI na tomto počítači dělá:",
		"newkey.done":      "Nový klíč je vytvořen. Staré 2 řádky už nefungují. Tady jsou nové:",
		"stopped":          "Zastaveno. Přes Remote Hands teď tento počítač nikdo neovládá.",
		"press.enter":      "Stiskněte Enter a okno se zavře.",
		"act.connected":    "připojení (%s)",
		"act.badkey":       "pokus o připojení se špatným nebo starým klíčem",
		"act.info":         "zjištění informací o systému",
		"act.run":          "příkaz: %s",
		"act.check":        "kontrola příkazu č. %d",
		"act.stop":         "zastavení příkazu č. %d",
		"act.read":         "čtení souboru %s",
		"act.write":        "zápis souboru %s",
		"act.edit":         "úprava souboru %s",
		"act.list":         "procházení složky %s",
		"act.screenshot":   "snímek obrazovky",
	},
}

type translator struct{ lang string }

func newTranslator(lang string) translator {
	if _, ok := messages[lang]; !ok {
		lang = "en"
	}
	return translator{lang: lang}
}

func (t translator) T(key string, args ...any) string {
	s, ok := messages[t.lang][key]
	if !ok {
		s = messages["en"][key]
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// pickLanguage chooses the window language: the --lang flag, then the
// REMOTE_HANDS_LANG environment variable, then the system language.
func pickLanguage(flagValue string) string {
	for _, v := range []string{flagValue, os.Getenv("REMOTE_HANDS_LANG"), systemLanguage()} {
		if l := normalizeLang(v); l != "" {
			return l
		}
	}
	return "en"
}

func normalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return ""
	case strings.HasPrefix(s, "zh"), s == "cn", s == "chinese", s == "中文":
		return "zh"
	case s == "cs" || strings.HasPrefix(s, "cs_") || strings.HasPrefix(s, "cs-"),
		s == "cz", s == "czech", s == "čeština", s == "cestina":
		return "cs"
	case strings.HasPrefix(s, "en"):
		return "en"
	}
	return ""
}
