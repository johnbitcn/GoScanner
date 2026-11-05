package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func TestMain(m *testing.M) {
	// Legacy terminal fixtures assert Chinese text; language tests override this baseline.
	os.Setenv("LC_ALL", "zh_CN.UTF-8")
	os.Exit(m.Run())
}

func TestLanguageSelection(t *testing.T) {
	for _, tc := range []struct {
		all, messages, lang string
		want                language
	}{
		{"", "", "", english},
		{"", "", "zh_CN.UTF-8", chinese},
		{"", "", "zh_SG.UTF-8", chinese},
		{"", "", "zh-Hans-CN.UTF-8", chinese},
		{"", "", "ZH_cn.UTF-8@modifier", chinese},
		{"", "", "zh_TW.UTF-8", english},
		{"", "", "C.UTF-8", english},
		{"", "", "fr_FR.UTF-8", english},
		{"C", "zh_CN", "zh_CN", english},
		{"en_US", "zh_CN", "zh_CN", english},
		{"zh_CN", "en_US", "en_US", chinese},
		{"", "zh_CN", "en_US", chinese},
		{"", "fr_FR", "zh_CN", english},
	} {
		t.Run(fmt.Sprintf("%s/%s/%s", tc.all, tc.messages, tc.lang), func(t *testing.T) {
			t.Setenv("LC_ALL", tc.all)
			t.Setenv("LC_MESSAGES", tc.messages)
			t.Setenv("LANG", tc.lang)
			t.Setenv("LANGUAGE", "zh_CN")
			if got := automaticLanguage(); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
			for _, explicit := range []struct {
				value string
				want  language
			}{{"en", english}, {"zh-CN", chinese}, {"auto", tc.want}} {
				if got, err := selectLanguage(explicit.value); err != nil || got != explicit.want {
					t.Fatalf("explicit %s: got %s, err=%v", explicit.value, got, err)
				}
			}
		})
	}
}

func TestHumanLanguageOptionsAndAgentIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LC_ALL", "en_US.UTF-8")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--help"}, "Usage"},
		{[]string{"--lang", "zh-CN", "--help"}, "用法"},
		{[]string{"--lang=zh-CN", "scan", "--help"}, "扫描区域"},
		{[]string{"scan", "--lang", "zh-CN", "--help"}, "扫描区域"},
		{[]string{"--lang", "zh-CN", "scan", "--lang=en", "--help"}, "Usage"},
		{[]string{"--lang=zh-CN", "--lang=auto", "info", "--help"}, "Scanner"},
	} {
		var out, stderr bytes.Buffer
		find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
			t.Fatal("help must not discover scanners")
			return nil, nil, nil
		}
		code := runWithDiscovery(context.Background(), tc.args, &out, &stderr, find)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("args=%v code=%d out=%q stderr=%q", tc.args, code, out.String(), stderr.String())
		}
	}
	for _, lang := range []string{"auto", "en", "zh-CN"} {
		result, code := agentRun(t, context.Background(), []string{"--lang", lang, "scan", "--help"}, nil)
		if code != 0 || result.Help != renderAgentHelp("scan") {
			t.Fatalf("language changed Agent help: %s %+v", lang, result)
		}
		result, code = agentRun(t, context.Background(), []string{"scan", "--lang", lang, "--dpi", "0"}, nil)
		if code != 2 || result.Error == nil || !strings.HasPrefix(result.Error.Message, "--dpi must be greater than zero.") {
			t.Fatalf("language changed Agent error: %s %+v", lang, result)
		}
	}
	for _, args := range [][]string{{"--lang"}, {"--lang="}, {"--lang", "fr"}, {"scan", "--lang", "fr"}, {"scan", "--lang"}} {
		var out, stderr bytes.Buffer
		if code := run(context.Background(), args, &out, &stderr); code != 2 || out.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("invalid language args=%v code=%d out=%q stderr=%q", args, code, out.String(), stderr.String())
		}
		result, code := agentRun(t, context.Background(), args, nil)
		if code != 2 || result.Error == nil || result.Error.Code != "invalid_arguments" {
			t.Fatalf("invalid Agent language args=%v code=%d result=%+v", args, code, result)
		}
	}
	var out, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--lang=zh-CN", "scan", "--lang=en", "--lang=fr"}, &out, &stderr); code != 2 || !strings.HasPrefix(stderr.String(), "Unsupported language") {
		t.Fatalf("invalid language lost the last valid selection: code=%d stderr=%q", code, stderr.String())
	}
	result, code := agentRun(t, context.Background(), []string{"scan", "--lang=zh-CN", "--lang=fr"}, nil)
	if code != 2 || result.Error == nil || !strings.HasPrefix(result.Error.Message, "Unsupported language") {
		t.Fatalf("flag parsing wrapped a project diagnostic as raw text: code=%d result=%+v", code, result)
	}
	// A flag value is user data, not another global option.
	r, _, failure, code := parseRequest([]string{"scan", "-o", "--lang.pdf"}, false, english)
	if code != 0 || failure != nil || r.output != "--lang.pdf" {
		t.Fatalf("language parsing consumed an output filename: %+v %+v", r, failure)
	}
}

func TestLanguageConfigurationPriority(t *testing.T) {
	for _, tc := range []struct {
		name, config, locale string
		args                 []string
		want                 language
	}{
		{"missing/en", "", "en_US", nil, english},
		{"missing/zh", "", "zh_CN", nil, chinese},
		{"omitted", `{}`, "zh_CN", nil, chinese},
		{"configured/zh", `{"language":"zh-CN"}`, "en_US", nil, chinese},
		{"configured/en", `{"language":"en"}`, "zh_CN", nil, english},
		{"configured/auto", `{"language":"auto"}`, "zh_CN", nil, chinese},
		{"unsupported-locale", `{"language":"auto"}`, "fr_FR", nil, english},
		{"leading/en", `{"language":"zh-CN"}`, "zh_CN", []string{"--lang=en", "scan"}, english},
		{"subcommand/zh", `{"language":"en"}`, "en_US", []string{"scan", "--lang", "zh-CN"}, chinese},
		{"leading/auto", `{"language":"zh-CN"}`, "en_US", []string{"--lang=auto", "scan"}, english},
		{"subcommand/auto", `{"language":"en"}`, "zh_CN", []string{"scan", "--lang", "auto"}, chinese},
		{"last-option", `{"language":"zh-CN"}`, "zh_CN", []string{"--lang=zh-CN", "scan", "--lang=en"}, english},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if tc.config != "" {
				writeTestConfig(t, tc.config)
			}
			t.Setenv("LC_ALL", tc.locale)
			args := tc.args
			if args == nil {
				args = []string{"scan"}
			}
			find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
				t.Fatal("language selection accessed scanners")
				return nil, nil, nil
			}
			var out, stderr bytes.Buffer
			helpArgs := append(slices.Clone(args), "--help")
			if code := runWithDiscovery(context.Background(), helpArgs, &out, &stderr, find); code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "\n"+tc.want.text("用法")+"\n") {
				t.Fatalf("help code=%d out=%q stderr=%q", code, out.String(), stderr.String())
			}
			out.Reset()
			stderr.Reset()
			invalidArgs := append(slices.Clone(args), "--dpi=0")
			prefix := "--dpi must"
			if tc.want == chinese {
				prefix = "--dpi 必须"
			}
			if code := runWithDiscovery(context.Background(), invalidArgs, &out, &stderr, find); code != 2 || out.Len() != 0 || !strings.HasPrefix(stderr.String(), prefix) {
				t.Fatalf("error code=%d out=%q stderr=%q", code, out.String(), stderr.String())
			}
			result, code := agentRun(t, context.Background(), invalidArgs, find)
			if code != 2 || result.Error == nil || !strings.HasPrefix(result.Error.Message, "--dpi must") {
				t.Fatalf("configuration changed Agent language: code=%d result=%+v", code, result)
			}
			if tc.args == nil {
				out.Reset()
				stderr.Reset()
				if code := runWithDiscovery(context.Background(), []string{"--help"}, &out, &stderr, find); code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "\n"+tc.want.text("用法")+"\n") {
					t.Fatalf("root help code=%d out=%q stderr=%q", code, out.String(), stderr.String())
				}
			}
		})
	}
}

func TestLanguageHelpWithInvalidConfiguration(t *testing.T) {
	t.Setenv("LC_ALL", "en_US")
	for _, tc := range []struct {
		data string
		want language
	}{
		{"broken config", english},
		{`null`, english},
		{`{"language":"fr"}`, english},
		{`{"language":123}`, english},
		{`{"language":"zh-CN","scan":{"dpi":"bad"}}`, chinese},
		{`{"language":"zh-CN","unknown":true}`, chinese},
	} {
		t.Run(tc.data, func(t *testing.T) {
			writeTestConfig(t, tc.data)
			var out, stderr bytes.Buffer
			if code := run(context.Background(), []string{"scan", "--help"}, &out, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "\n"+tc.want.text("用法")+"\n") {
				t.Fatalf("help code=%d out=%q stderr=%q", code, out.String(), stderr.String())
			}
			result, code := agentRun(t, context.Background(), []string{"scan"}, nil)
			if code != 2 || result.Error == nil || result.Error.Code != "invalid_config" || !strings.HasPrefix(result.Error.Message, "Cannot parse the configuration file") {
				t.Fatalf("invalid config code=%d result=%+v", code, result)
			}
			result, code = agentRun(t, context.Background(), []string{"scan", "--help"}, nil)
			if code != 0 || result.Help != renderAgentHelp("scan") {
				t.Fatal("invalid configuration prevented Agent help", code, result)
			}
		})
	}
	t.Setenv("HOME", t.TempDir())
	path, _ := configPath()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got := configuredLanguage(); got != english {
		t.Fatal("non-regular configuration did not fall back", got)
	}
}

func TestLanguageHelpSurvivesBrokenConfigWithoutWritingUserData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, _ := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken config"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "zh-CN"} {
		var out, stderr bytes.Buffer
		find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
			t.Fatal("language selection or help accessed a scanner")
			return nil, nil, nil
		}
		if code := runWithDiscovery(context.Background(), []string{"--lang", lang, "scan", "--help"}, &out, &stderr, find); code != 0 || stderr.Len() != 0 {
			t.Fatalf("language=%s code=%d stderr=%q", lang, code, stderr.String())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "broken config" {
		t.Fatal("language selection modified configuration", err)
	}
	registry, _ := drivesPath()
	if _, err := os.Stat(registry); !os.IsNotExist(err) {
		t.Fatal("language selection or help wrote scanner records", err)
	}
}

func TestHumanScanLanguagePreservesAcquisitionAndPaths(t *testing.T) {
	data, image := fixture(t), jpegSample(t)
	var jobs atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request, action string, _ []byte) {
		switch action {
		case "GetScannerElements":
			w.Write(data)
		case "CreateScanJob":
			jobs.Add(1)
			io.WriteString(w, responseXML("CreateScanJobResponse", "<w:JobId>7</w:JobId><w:JobToken>token</w:JobToken>"))
		case "RetrieveImage":
			ct, body := mtom(image, "boundary", "cid:image@scanner")
			w.Header().Set("Content-Type", ct)
			w.Write(body)
		default:
			t.Error("unexpected action", action)
		}
	})
	for _, lang := range []language{chinese, english} {
		var out, stderr bytes.Buffer
		path := filepath.Join(t.TempDir(), "彩色.jpg")
		args := []string{"scan", "--lang", lang.String(), "--endpoint", c.endpoint, "--manual=false", "-o", path}
		if code := run(context.Background(), args, &out, &stderr); code != 0 || !strings.Contains(out.String(), path) {
			t.Fatalf("language=%s code=%d out=%q stderr=%q", lang, code, out.String(), stderr.String())
		}
		if lang == english {
			if !strings.Contains(out.String(), "Saved:") || !strings.Contains(stderr.String(), "Do not turn off the scanner.") {
				t.Fatalf("English scan text: out=%q stderr=%q", out.String(), stderr.String())
			}
			assertNoChinese(t, strings.ReplaceAll(out.String(), path, "OUTPUT"))
			assertNoChinese(t, stderr.String())
		} else if !strings.Contains(out.String(), "已保存：") || !strings.Contains(stderr.String(), "请勿断电") {
			t.Fatalf("Chinese scan text: out=%q stderr=%q", out.String(), stderr.String())
		}
		assertSavedImage(t, path, image)
	}
	if jobs.Load() != 2 {
		t.Fatal("language changed scan count", jobs.Load())
	}
}

func TestHumanDiscoveryJSONDoesNotTranslateDeviceData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	find := func(context.Context) ([]scannerDevice, []diagnostic, error) {
		return []scannerDevice{{Name: "彩色", Model: "开启", Protocol: "wsd", Endpoint: "http://192.0.2.8/wsd/scan"}}, nil, nil
	}
	for _, lang := range []language{chinese, english} {
		var out, stderr bytes.Buffer
		if code := runWithDiscovery(context.Background(), []string{"discover", "--lang", lang.String()}, &out, &stderr, find); code != 0 || stderr.Len() != 0 {
			t.Fatalf("language=%s code=%d stderr=%q", lang, code, stderr.String())
		}
		var devices []scannerDevice
		if err := json.Unmarshal(out.Bytes(), &devices); err != nil || len(devices) != 1 || devices[0].Name != "彩色" || devices[0].Model != "开启" || devices[0].Protocol != "wsd" {
			t.Fatalf("language=%s output=%q err=%v", lang, out.String(), err)
		}
		text := resultText(commandResult{Devices: &devices}, false, lang)
		if !strings.Contains(text, "彩色 · 开启 · http://192.0.2.8/wsd/scan") {
			t.Fatal("TUI translated external data as catalog keys", text)
		}
	}
}

func TestHumanHelpTranslationsAndLayout(t *testing.T) {
	for _, lang := range []language{chinese, english} {
		for _, command := range []string{"", "scan", "info", "discover"} {
			for _, width := range []int{24, 40, 72, 80, 140} {
				plain := renderHelp(command, width, false, lang)
				colored := renderHelp(command, width, true, lang)
				if ansi.Strip(colored) != plain {
					t.Fatalf("style changed %s %s help", lang, command)
				}
				for _, line := range strings.Split(plain, "\n") {
					if ansi.StringWidth(line) > min(width, 88) {
						t.Fatalf("%s %s width=%d line=%q", lang, command, width, line)
					}
				}
				if lang == english {
					assertNoChinese(t, plain)
				}
			}
		}
	}
}

func TestHumanDiagnosticsAndRawData(t *testing.T) {
	path := "/扫描件/合同.pdf"
	warning := diagnosticf("Processing skipped.", "已跳过处理")
	result := commandResult{Status: "ok", Files: []string{path}, Warnings: []diagnostic{warning}}
	result.fail("scan_failed", diagnosticf("Cannot save %q: %w", "无法保存 %q：%w", path, errOutputFormat))
	for _, lang := range []language{chinese, english} {
		var out, stderr bytes.Buffer
		if code := writeResult(&out, &stderr, result, 1, false, lang); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), path) {
			t.Fatalf("language=%s output=%q stderr=%q", lang, out.String(), stderr.String())
		}
		want, warningText := "无法保存", "已跳过处理"
		if lang == english {
			want, warningText = "Cannot save", "Processing skipped."
		}
		text := resultText(result, false, lang)
		if !strings.Contains(stderr.String(), want) || !strings.Contains(text, path) || !strings.Contains(text, warningText) {
			t.Fatalf("language=%s text=%q stderr=%q", lang, text, stderr.String())
		}
		var jsonOut, jsonErr bytes.Buffer
		writeResult(&jsonOut, &jsonErr, result, 1, true, lang)
		var decoded commandResult
		if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil || !strings.HasPrefix(decoded.Error.Message, "Cannot save") || decoded.Files[0] != path {
			t.Fatalf("language=%s Agent JSON=%q err=%v", lang, jsonOut.String(), err)
		}
	}
}

func TestHumanModelUsesRequestLanguage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, lang := range []language{chinese, english} {
		r := commandRequest{command: "scan", language: lang, output: "scan.pdf", area: "a4", opts: defaultOptions()}
		m := newHumanModel(context.Background(), func() {}, r, nil)
		m.width, m.height = 80, 40
		for _, state := range []string{"ready", "unavailable", "device-list", "devices", "settings", "working", "waiting", "done"} {
			m.state = state
			text := ansi.Strip(m.View().Content)
			if lang == english {
				assertNoChinese(t, text)
			}
		}
		m.editSettings()
		m.capabilities = &info{WidthsDPI: []int{300}, HeightsDPI: []int{300}, Colors: []string{"RGB24", "Grayscale8"}, Formats: []string{formatJPEG}}
		m.field = settingMode
		choices := m.settingChoices()
		if len(choices) != 2 || choices[0].value != "color" || choices[1].value != "gray" {
			t.Fatal("language changed scan mode values", choices)
		}
		m.choices, m.choice = choices, 1
		m.confirmSettingChoice()
		if m.draft.opts.Mode != "gray" {
			t.Fatal("translated setting changed the selected mode")
		}
	}
}

func assertNoChinese(t *testing.T, text string) {
	t.Helper()
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || r >= '\uff00' && r <= '\uffef' {
			t.Fatalf("untranslated project text: %q", text)
		}
	}
}

func TestHumanCatalogCoversSourceTemplates(t *testing.T) {
	verbs := regexp.MustCompile(`%[%a-zA-Z]`)
	for source, translated := range humanEnglish {
		if !slices.Equal(verbs.FindAllString(source, -1), verbs.FindAllString(translated, -1)) {
			t.Errorf("translation changed formatting arguments: %q -> %q", source, translated)
		}
		if translated == "" {
			t.Errorf("empty translation for %q", source)
		}
		assertNoChinese(t, translated)
	}
	paths := []string{"tui.go", "tui_devices.go", "tui_device_list.go", "tui_settings.go", "pages.go", "device_selection.go", "keyboard.go", "help.go", "i18n.go", "command.go"}
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "text" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			source, _ := strconv.Unquote(literal.Value)
			if _, ok := humanEnglish[source]; !ok {
				t.Errorf("%s: missing translation for %q", path, source)
			}
			return true
		})
	}
	for _, command := range []string{"", "scan", "info", "discover"} {
		_, sections := helpContent(command, chinese)
		for _, section := range sections {
			for _, item := range section.items {
				for _, source := range []string{section.title, item.text, item.defaultValue} {
					if strings.Contains(source, "goscanner [--agent] "+command+" [参数]") && command != "" {
						continue
					}
					if english.text(source) == source && strings.IndexFunc(source, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
						t.Errorf("missing help translation for %q", source)
					}
				}
			}
		}
	}
}
