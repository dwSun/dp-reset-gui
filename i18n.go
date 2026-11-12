package main

// 国际化（i18n）：用 Fyne 自带的 fyne.io/fyne/v2/lang（内部是 go-i18n）。
//
// 语言由系统 locale 自动选择：zh_CN → 中文；其它 → 英文。
// 翻译包是 translations/{en,zh_CN}.json，文件名必须带完整 locale（实测 zh.json
// 不会被 zh_CN 匹配）。代码里 tr() 的第二个参数是英文兜底，所以即使翻译包
// 缺失或者 key 没覆盖，界面也不会退化成 key 本身。

import (
	"embed"
	"log"
	"strings"

	"fyne.io/fyne/v2/lang"
)

//go:embed translations
var translationsFS embed.FS

// loadTranslations 注册内嵌翻译包，必须在任何 tr() 之前调用一次。
func loadTranslations() {
	if err := lang.AddTranslationsFS(translationsFS, "translations"); err != nil {
		log.Printf("load translations: %v", err)
	}
}

// tr 按 id 取翻译文案；fallback 为英文兜底，data 可选（模板数据，如 map[string]any）。
func tr(id, fallback string, data ...any) string {
	return lang.X(id, fallback, data...)
}

// isChineseLocale 判断当前系统 locale 是否中文（用于字体/助手语言等场景）。
func isChineseLocale() bool {
	return strings.HasPrefix(strings.ToLower(string(lang.SystemLocale())), "zh")
}
