package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/rivo/uniseg"
)

type helpItem struct {
	label, text, defaultValue string
}

type helpSection struct {
	title string
	items []helpItem
}

func helpDisplay(out io.Writer) (width int, colored bool) {
	width = 80
	file, ok := out.(*os.File)
	if !ok || !term.IsTerminal(file.Fd()) {
		return width, false
	}
	if columns, _, err := term.GetSize(file.Fd()); err == nil && columns > 0 {
		width = min(columns, 88)
	}
	colored = os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && colorprofile.Detect(out, os.Environ()) >= colorprofile.ANSI
	return width, colored
}

type helpLine struct {
	text  string
	start int
}

// Preserve source offsets so inline defaults can be styled after Unicode wrapping.
func wrapHelp(text string, width int) []helpLine {
	var lines []helpLine
	line, start := "", 0
	flush := func() {
		if trimmed := strings.TrimRight(line, " "); trimmed != "" {
			lines = append(lines, helpLine{trimmed, start})
		}
		line = ""
	}
	rest, state := text, -1
	for rest != "" {
		offset := len(text) - len(rest)
		segment, next, mustBreak, nextState := uniseg.FirstLineSegmentInString(rest, state)
		rest, state = next, nextState
		segment = strings.TrimRight(segment, "\r\n")
		if ansi.StringWidth(strings.TrimRight(line+segment, " ")) > width && line != "" {
			flush()
		}
		if line == "" {
			trimmed := strings.TrimLeft(segment, " ")
			offset += len(segment) - len(trimmed)
			segment, start = trimmed, offset
		}
		if ansi.StringWidth(strings.TrimRight(segment, " ")) > width {
			for i, part := range strings.Split(ansi.Hardwrap(segment, width, true), "\n") {
				if i > 0 {
					flush()
				}
				line, start = part, offset
				offset += len(part)
			}
		} else {
			line += segment
		}
		if mustBreak {
			flush()
		}
	}
	flush()
	return lines
}

func renderHelp(command string, width int, colored bool) string {
	if width <= 0 {
		width = 80
	}
	width = max(8, min(width, 88))
	summary, sections := helpContent(command)
	paint := func(text string, style lipgloss.Style) string {
		if colored {
			return style.Render(text)
		}
		return text
	}
	var out strings.Builder
	plain, bold := lipgloss.NewStyle(), lipgloss.NewStyle().Bold(true)
	write := func(text string, indent int, style lipgloss.Style) {
		for _, line := range wrapHelp(text, max(1, width-indent)) {
			out.WriteString(strings.Repeat(" ", indent) + paint(line.text, style) + "\n")
		}
	}
	write("GoScanner", 0, tuiAccent)
	write(summary, 0, plain)
	for _, section := range sections {
		out.WriteByte('\n')
		write(section.title, 0, tuiAccent)
		for i, item := range section.items {
			if item.label == "" {
				write(item.text, 2, plain)
				continue
			}
			text := item.text
			if item.defaultValue != "" {
				text += " 默认：" + item.defaultValue + "。"
			}
			defaultStart := len(item.text) + 1
			body := func(line helpLine) string {
				if item.defaultValue == "" || defaultStart >= line.start+len(line.text) {
					return line.text
				}
				index := max(0, defaultStart-line.start)
				return line.text[:index] + paint(line.text[index:], tuiMuted)
			}
			label := paint(item.label, bold)
			column := 24
			if command == "" {
				column = 16
			}
			multiline := false
			if width < 72 || ansi.StringWidth(item.label)+4 > column {
				write(item.label, 2, bold)
				for _, line := range wrapHelp(text, width-4) {
					out.WriteString("    " + body(line) + "\n")
				}
				multiline = true
			} else {
				lines := wrapHelp(text, width-column)
				for j, line := range lines {
					if j == 0 {
						out.WriteString("  " + label + strings.Repeat(" ", column-2-ansi.StringWidth(item.label)))
					} else {
						out.WriteString(strings.Repeat(" ", column))
					}
					out.WriteString(body(line) + "\n")
				}
				multiline = len(lines) > 1
			}
			if multiline && i < len(section.items)-1 {
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

func helpContent(command string) (string, []helpSection) {
	if command == "" {
		return "macOS 局域网 eSCL / WSD 扫描工具", []helpSection{
			{"用法", []helpItem{{text: "goscanner"}, {text: "goscanner [--agent] <命令> [参数]"}}},
			{"命令", []helpItem{
				{"scan", "扫描文档；默认打开交互界面", ""},
				{"info", "查看设备能力与状态，不扫描", ""},
				{"discover", "发现局域网设备，不切换默认设备", ""},
			}},
			{"常用示例", []helpItem{{text: "goscanner"}, {text: "goscanner scan -o 文档.pdf"}, {text: "goscanner --agent info"}}},
			{"全局选项", []helpItem{
				{"--agent", "无交互模式，输出 JSON；必须放在命令前", ""},
				{"-h, --help", "显示帮助", ""},
			}},
			{"配置", []helpItem{{text: "~/.config/goscanner/config.json"}}},
			{"查看命令参数", []helpItem{{text: "goscanner <命令> --help"}}},
		}
	}
	defaults := defaultConfig()
	summary := map[string]string{"scan": "扫描文档", "info": "查看设备能力与状态，不启动扫描", "discover": "并行发现局域网 eSCL 和 WSD 扫描设备"}[command]
	sections := []helpSection{{"用法", []helpItem{{text: "goscanner [--agent] " + command + " [参数]"}}}}
	if command != "discover" {
		sections = append(sections, helpSection{"设备", []helpItem{
			{"--host IP", "扫描仪 IP 地址，不接受主机名或 URL。未配置时可在交互界面选用或添加设备。", ""},
			{"--protocol NAME", "可选 wsd 或 escl，仅覆盖本次操作。不指定时使用设备列表首选协议；首次确认双协议时首选 eSCL。", "设备列表首选"},
			{"--scheme NAME", "http 或 https；WSD 仅使用 http，eSCL 支持 https 并正常验证证书。", "http"},
			{"--port PORT", "高级地址参数；配合 --host 指定已知扫描服务端口。", ""},
			{"--path PATH", "高级地址参数；配合 --host 指定已知路径，以 / 开头。只给 IP 时由发现取得地址。", ""},
			{text: "TUI 手动添加只输入 IP。已登记设备使用首选协议，未登记 IP 通过发现取得实际地址；发现失败时不猜端口或路径。"},
		}})
	}
	if command == "scan" {
		sections = append(sections,
			helpSection{"扫描", []helpItem{
				{"--dpi DPI", "扫描分辨率，必须为设备支持的值。", fmt.Sprint(defaults.Scan.DPI)},
				{"--mode MODE", "色彩模式：color（彩色）、gray（灰度）。", defaults.Scan.Mode},
				{"--area AREA", "扫描区域：a4、full，或 x,y,width,height（毫米）。坐标原点为平板左上角。", defaults.Scan.Area},
			}},
			helpSection{"输出", []helpItem{
				{"-o FILE", "输出文件，支持 JPEG、TIFF、PNG、PDF。扩展名决定格式；未指定时按配置生成时间戳文件名。", ""},
				{"--single-page", "将 PDF 每页保存为独立文件；连续扫描时文件名追加页码。仅适用于 PDF。", "关闭"},
			}},
			helpSection{"连续扫描", []helpItem{
				{"--manual[=BOOL]", "连续换页扫描。使用 --manual=false 关闭；Agent 模式不支持。", "TUI 开启，文本模式关闭"},
				{text: "交互界面按 Enter 继续下一页、Esc 结束；文本模式按任意键继续。换页等待不计入超时。"},
			}},
			helpSection{"图像处理", []helpItem{
				{"--deskew", "AI 自动纠偏，额外保留设备原图。", "关闭"},
				{"--autocrop", "AI 自动裁切，额外保留设备原图。", "关闭"},
				{text: "可用 --deskew=false、--autocrop=false 临时关闭配置中启用的处理。"},
			}},
		)
	}
	timeout := helpItem{"--timeout DURATION", "读取设备能力和状态的超时。", "2m"}
	if command == "scan" {
		timeout.text, timeout.defaultValue = "每页扫描超时，不含选设备和换页等待。失败清理最多额外等待 10 秒。", defaultScanTimeout.String()
	}
	if command == "discover" {
		timeout.text, timeout.defaultValue = "局域网设备发现超时。", "10s"
	}
	sections = append(sections, helpSection{"超时", []helpItem{timeout}})
	if command != "discover" {
		sections = append(sections, helpSection{"兼容参数", []helpItem{
			{"--endpoint URL", "完整 HTTP/HTTPS 扫描端点；已登记设备使用首选协议。--protocol 可选，仅覆盖本次；与拆分地址参数互斥。", ""},
		}})
	}
	sections = append(sections, helpSection{"帮助", []helpItem{{"-h, --help", "显示当前命令的帮助", ""}}})
	switch command {
	case "scan":
		sections = append(sections,
			helpSection{"示例", []helpItem{{text: "goscanner scan -o 文档.pdf"}, {text: "goscanner scan --manual=false -o 单页.png"}, {text: "goscanner --agent scan -o 单页.pdf"}}},
			helpSection{"默认行为", []helpItem{
				{text: "未指定 -o 时，默认在当前目录生成 scan-YYYYMMDD-HHMMSS.jpg；连续图片追加 -001 等页码，PDF 默认合并。目录与格式可由配置覆盖。"},
				{text: "WSD 的 TIFF 使用设备原生未压缩格式，PNG 由 TIFF 无损转换。eSCL 按设备能力选择图像格式；JPEG 转 PNG 不能恢复已有压缩损失。"},
				{text: "交互界面先只读检查设备状态和参数，通过后按 Enter 才扫描。按 r 重查、s 修改设置、d 更换设备；设备列表按 p 切换并保存首选协议。"},
			}},
			helpSection{"安全", []helpItem{
				{text: "已有文件不会覆盖；协议连接失败不自动切换，扫描任务不自动重试。Ctrl+C 尝试取消本客户端任务。"},
				{text: "合并 PDF 在正常结束、中断或后续页失败时尝试保存已完成页面。"},
			}},
			helpSection{"Agent 模式", []helpItem{{text: "--agent 必须放在命令前。仅输出一个 JSON 结果，不交互、不写配置或设备表。只给未登记 IP 时会按 IP 筛选发现；未提供地址时不自动发现。须提供设备地址或配置默认设备，不支持 --manual。"}}},
		)
	case "info":
		sections = append(sections,
			helpSection{"示例", []helpItem{{text: "goscanner info"}, {text: "goscanner info --host 192.168.1.8"}, {text: "goscanner --agent info"}}},
			helpSection{"行为", []helpItem{{text: "只读查询设备，不启动扫描。普通模式记录能力与状态到 drives.json；明确选择并验证设备后，才保存默认记录引用。"}}},
			helpSection{"Agent 模式", []helpItem{{text: "--agent 必须放在命令前。仅输出一个 JSON 结果，不交互、不写配置或设备表。只给未登记 IP 时会按 IP 筛选发现；未提供地址时返回 device_required。"}}},
		)
	case "discover":
		sections = append(sections,
			helpSection{"示例", []helpItem{{text: "goscanner discover"}, {text: "goscanner discover --timeout 5s"}, {text: "goscanner --agent discover"}}},
			helpSection{"行为", []helpItem{{text: "只发现设备，不启动扫描、不选择或修改默认设备。普通模式将发现信息记录到 drives.json。"}}},
			helpSection{"Agent 模式", []helpItem{{text: "--agent 必须放在命令前。仅输出一个 JSON 结果，不交互、不写配置或设备表。"}}},
		)
	}
	if command != "discover" {
		sections = append(sections, helpSection{"配置", []helpItem{
			{text: "~/.config/goscanner/config.json"},
			{text: "扫描参数：显式命令行参数 > 配置文件 > 内置默认值。默认设备用 device.record_id 引用设备表，协议默认使用设备列表首选设置。"},
			{text: "引用失效返回 device_record_not_found；设备身份明确冲突返回 device_identity_conflict。重新选择设备，不会自动改绑或重扫。"},
		}})
	}
	return summary, sections
}
