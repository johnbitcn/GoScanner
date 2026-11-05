package main

import (
	"os"
	"strings"
)

type language uint8

const (
	chinese language = iota
	english
)

func (l language) String() string {
	if l == english {
		return "en"
	}
	return "zh-CN"
}

func automaticLanguage() language {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value := os.Getenv(key); value != "" {
			value = strings.ToLower(strings.ReplaceAll(value, "_", "-"))
			value, _, _ = strings.Cut(value, ".")
			value, _, _ = strings.Cut(value, "@")
			if value == "zh" || value == "zh-cn" || value == "zh-sg" || value == "zh-hans" || strings.HasPrefix(value, "zh-hans-") {
				return chinese
			}
			return english
		}
	}
	return english
}

func selectLanguage(value string) (language, error) {
	switch value {
	case "auto":
		return automaticLanguage(), nil
	case "en":
		return english, nil
	case "zh-CN":
		return chinese, nil
	default:
		return automaticLanguage(), diagnosticf("Unsupported language %q. Use auto, en, or zh-CN.", "不支持语言 %q；请使用 auto、en 或 zh-CN", value)
	}
}

// Only consume leading global options; subcommand options use flag.FlagSet.
func leadingLanguage(args []string) ([]string, language, error) {
	lang := automaticLanguage()
	for len(args) > 0 {
		value := ""
		if args[0] == "--lang" {
			if len(args) < 2 {
				return nil, lang, diagnosticf("--lang requires auto, en, or zh-CN.", "--lang 需要指定 auto、en 或 zh-CN")
			}
			value, args = args[1], args[2:]
		} else if strings.HasPrefix(args[0], "--lang=") {
			value, args = strings.TrimPrefix(args[0], "--lang="), args[1:]
		} else {
			break
		}
		selected, err := selectLanguage(value)
		if err != nil {
			return nil, lang, err
		}
		lang = selected
	}
	return args, lang, nil
}

func (l language) text(source string) string {
	if l == english {
		if translated, ok := humanEnglish[source]; ok {
			return translated
		}
	}
	return source
}

func (l language) error(err error) string {
	if l == english {
		return agentError(err)
	}
	return err.Error()
}

func (l language) commandError(err *commandError) string {
	if err.cause != nil {
		return l.error(err.cause)
	}
	return err.Message
}

func (l language) warnings(warnings []diagnostic) string {
	parts := make([]string, len(warnings))
	for i, warning := range warnings {
		parts[i] = l.error(warning)
	}
	return strings.Join(parts, l.text("；"))
}
