package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func selectSetting(t *testing.T, m *humanModel, field int) {
	t.Helper()
	if m.state != "settings" || m.editing || len(m.choices) != 0 {
		t.Fatal("not in settings list")
	}
	for m.field > field {
		press(m, tea.KeyUp)
	}
	for m.field < field {
		press(m, tea.KeyDown)
	}
}

func chooseSetting(t *testing.T, m *humanModel, field int, label string) {
	t.Helper()
	selectSetting(t, m, field)
	press(m, tea.KeyEnter)
	index := -1
	for i, choice := range m.choices {
		if choice.label == label {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("missing choice %q: %+v notice=%s", label, m.choices, m.notice)
	}
	for m.choice > index {
		press(m, tea.KeyUp)
	}
	for m.choice < index {
		press(m, tea.KeyDown)
	}
	press(m, tea.KeyEnter)
}

func TestHumanSettingsListAndChoices(t *testing.T) {
	m := humanTestModel(t)
	press(m, 's')
	for _, name := range settingNames {
		if !strings.Contains(m.View().Content, name) {
			t.Fatalf("setting %q is not visible", name)
		}
	}
	chooseSetting(t, m, settingDPI, "300 DPI")
	chooseSetting(t, m, settingMode, "灰度")
	chooseSetting(t, m, settingArea, "完整平板")
	selectSetting(t, m, settingPath)
	press(m, tea.KeyEnter)
	if !m.editing {
		t.Fatal("path must remain editable")
	}
	path := filepath.Join(t.TempDir(), "自定义.pdf")
	m.input.SetValue("")
	m.Update(tea.PasteMsg{Content: path})
	press(m, tea.KeyEnter)
	if m.editing || m.draft.output != path || m.request.opts.DPI != 150 {
		t.Fatal("draft was not isolated", m.draft.output)
	}
	selectSetting(t, m, settingApply)
	press(m, tea.KeyEnter)
	if !m.working {
		t.Fatal("settings changed without rechecking device")
	}
	m.Update(m.listen())
	if m.state != "ready" || m.request.opts.DPI != 300 || m.request.opts.Mode != "gray" || m.request.area != "full" || m.request.output != path {
		t.Fatalf("request=%+v state=%s notice=%s", m.request, m.state, m.notice)
	}
}

func TestHumanSettingsFormatAndBooleans(t *testing.T) {
	m := humanTestModel(t)
	press(m, 's')
	chooseSetting(t, m, settingSinglePage, "开启")
	chooseSetting(t, m, settingFormat, "PNG")
	if m.draft.output != "scan.png" || m.draft.singlePage || !strings.Contains(m.notice, "已关闭") {
		t.Fatal("format change left PDF-only settings enabled")
	}
	selectSetting(t, m, settingSinglePage)
	press(m, tea.KeyEnter)
	if len(m.choices) != 0 || m.notice == "" {
		t.Fatal("PDF-only setting enabled for PNG")
	}
	press(m, tea.KeySpace)
	if m.draft.singlePage {
		t.Fatal("Space enabled PDF-only setting for PNG")
	}
	for _, field := range []int{settingManual, settingDeskew, settingAutocrop} {
		selectSetting(t, m, field)
		press(m, tea.KeySpace)
		if !*m.settingToggle() {
			t.Fatal("Space did not enable option")
		}
		chooseSetting(t, m, field, "关闭")
		if *m.settingToggle() {
			t.Fatal("choice did not disable option")
		}
	}
	chooseSetting(t, m, settingFormat, "PDF")
	chooseSetting(t, m, settingSinglePage, "开启")
	if m.draft.output != "scan.pdf" || !m.draft.singlePage {
		t.Fatal("PDF settings not restored")
	}
	m.draft.output = "scan.JPEG"
	chooseSetting(t, m, settingFormat, "JPEG")
	if m.draft.output != "scan.JPEG" {
		t.Fatal("unchanged format renamed the output")
	}
}

func TestHumanSettingsDeviceChoices(t *testing.T) {
	m := humanTestModel(t)
	m.capabilities = &info{WidthsDPI: []int{300, 150, 300}, HeightsDPI: []int{300}, Colors: []string{"Grayscale8", "BlackAndWhite1"}, Formats: []string{formatJPEG}}
	press(m, 's')
	for _, tc := range []struct {
		field  int
		labels []string
	}{
		{settingDPI, []string{"300 DPI"}},
		{settingMode, []string{"灰度"}},
		{settingFormat, []string{"JPEG", "PDF"}},
	} {
		selectSetting(t, m, tc.field)
		press(m, tea.KeyEnter)
		var labels []string
		for _, choice := range m.choices {
			labels = append(labels, choice.label)
		}
		if !slices.Equal(labels, tc.labels) {
			t.Fatalf("choices=%v want=%v", labels, tc.labels)
		}
		press(m, tea.KeyEsc)
	}
	if !slices.Equal(m.capabilities.WidthsDPI, []int{300, 150, 300}) {
		t.Fatal("editing mutated device capabilities")
	}
	m.capabilities = nil
	selectSetting(t, m, settingDPI)
	press(m, tea.KeyEnter)
	if len(m.choices) != 0 || m.notice == "" {
		t.Fatal("invented capabilities without a device query")
	}
}

func TestHumanSettingsCustomInputAndDiscard(t *testing.T) {
	m := humanTestModel(t)
	press(m, 's')
	chooseSetting(t, m, settingArea, "自定义区域…")
	if !m.editing {
		t.Fatal("custom region did not open input")
	}
	m.input.SetValue("NaN,0,100,100")
	press(m, tea.KeyEnter)
	if !m.editing || m.notice == "" || m.draft.area != "a4" {
		t.Fatal("invalid custom region was accepted")
	}
	press(m, tea.KeyEsc)
	if m.state != "settings" || m.editing || m.draft.area != "a4" {
		t.Fatal("Esc must discard only this input")
	}
	chooseSetting(t, m, settingArea, "自定义区域…")
	m.input.SetValue("0,0,100,100")
	press(m, tea.KeyEnter)
	if m.editing || m.draft.area != "0,0,100,100" {
		t.Fatal("custom region not accepted")
	}
	selectSetting(t, m, settingPath)
	press(m, tea.KeyEnter)
	m.input.SetValue("unsupported.gif")
	press(m, tea.KeyEnter)
	if !m.editing || m.notice == "" || m.draft.output != "scan.pdf" {
		t.Fatal("invalid output was accepted")
	}
	press(m, tea.KeyEsc)
	chooseSetting(t, m, settingDPI, "300 DPI")
	selectSetting(t, m, settingManual)
	press(m, tea.KeySpace)
	press(m, tea.KeyEsc)
	m.Update(m.listen())
	if m.state != "ready" || m.request.manual || m.request.opts.DPI != 150 || m.request.area != "a4" {
		t.Fatal("discarded settings were applied")
	}
}

func TestHumanLanguageAutonyms(t *testing.T) {
	for _, lang := range []language{chinese, english} {
		m := humanModel{lang: lang, field: settingLanguage}
		want := []settingChoice{{lang.text("自动（系统语言）"), "auto"}, {"English", "en"}, {"简体中文", "zh-CN"}}
		if choices := m.settingChoices(); !slices.Equal(choices, want) {
			t.Fatalf("language=%s choices=%+v want=%+v", lang, choices, want)
		}
		for _, choice := range want {
			m.draftLanguage = choice.value
			if got := m.settingValues()[settingLanguage]; got != choice.label {
				t.Fatalf("language=%s preference=%s label=%q want=%q", lang, choice.value, got, choice.label)
			}
		}
	}
}

func TestHumanSettingsLanguageConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, preference, label string
		explicit                bool
		initial, want           language
	}{
		{"switch", "en", "English", false, chinese, english},
		{"switch-chinese", "zh-CN", "简体中文", false, english, chinese},
		{"explicit-en", "zh-CN", "简体中文", true, english, english},
		{"explicit-auto", "en", "English", true, chinese, chinese},
		{"automatic", "auto", "自动（系统语言）", false, chinese, english},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := humanTestModel(t)
			path, _ := configPath()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte(`{"language":"auto","scan":{"timeout":"5m"}}`)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LC_ALL", "en_US")
			m.lang, m.request.language, m.request.languageExplicit = tc.initial, tc.initial, tc.explicit
			press(m, 's')
			label := tc.label
			if tc.preference == "auto" {
				label = m.lang.text(label)
			}
			chooseSetting(t, m, settingLanguage, label)
			if data, _ := os.ReadFile(path); string(data) != string(original) || m.lang != tc.initial || m.request.language != tc.initial {
				t.Fatal("selecting a language applied or saved the draft")
			}
			selectSetting(t, m, settingApply)
			press(m, tea.KeyEnter)
			if !m.working {
				t.Fatal("saved settings did not recheck device", m.notice)
			}
			m.Update(m.listen())
			cfg, err := loadConfig()
			if err != nil || cfg.Language != tc.preference || cfg.Scan.Timeout != "5m" || m.state != "ready" || m.lang != tc.want || m.request.language != tc.want {
				t.Fatalf("state=%s lang=%s request=%+v cfg=%+v err=%v", m.state, m.lang, m.request, cfg, err)
			}
			if m.lang == english {
				assertNoChinese(t, m.View().Content)
			}
		})
	}
}

func TestHumanSettingsCancelAndSaveFailure(t *testing.T) {
	m := humanTestModel(t)
	path, _ := configPath()
	original := []byte(`{"language":"auto"}`)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	before := m.request
	press(m, 's')
	chooseSetting(t, m, settingLanguage, "English")
	chooseSetting(t, m, settingDPI, "300 DPI")
	press(m, tea.KeyEsc)
	m.Update(m.listen())
	if data, _ := os.ReadFile(path); string(data) != string(original) || m.request.opts.DPI != before.opts.DPI || m.lang != before.language {
		t.Fatal("Esc applied or saved changes")
	}
	press(m, 's')
	chooseSetting(t, m, settingLanguage, "English")
	chooseSetting(t, m, settingDPI, "300 DPI")
	if err := os.WriteFile(path, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	selectSetting(t, m, settingApply)
	press(m, tea.KeyEnter)
	if m.state != "settings" || m.working || m.lang != before.language || m.request.opts.DPI != before.opts.DPI || !strings.Contains(m.notice, "保存设置失败") {
		t.Fatalf("save failure applied a draft: state=%s notice=%s request=%+v", m.state, m.notice, m.request)
	}
	if data, _ := os.ReadFile(path); string(data) != "broken JSON" {
		t.Fatal("invalid configuration was overwritten")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	press(m, tea.KeyEnter)
	m.Update(m.listen())
	if m.state != "ready" || m.request.opts.DPI != 300 {
		t.Fatal("draft could not be saved after repairing configuration", m.notice)
	}
}

func TestHumanSettingsSmallWindow(t *testing.T) {
	m := humanTestModel(t)
	press(m, 's')
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	selectSetting(t, m, settingApply)
	view := m.View().Content
	if !strings.Contains(view, "保存并应用") || len(strings.Split(view, "\n")) > 12 {
		t.Fatal("settings cursor is not visible in small terminal", view)
	}
	selectSetting(t, m, settingDPI)
	press(m, tea.KeyEnter)
	for m.choice < len(m.choices)-1 {
		press(m, tea.KeyDown)
	}
	if view := m.View().Content; !strings.Contains(view, "300 DPI") || len(strings.Split(view, "\n")) > 12 {
		t.Fatal("choice cursor is not visible in small terminal", view)
	}
}
