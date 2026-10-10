package maintenancepage

import (
	"strconv"
	"strings"
)

// The page speaks English and Arabic. {b} is replaced by the title, {d} by a
// duration.
var messages = map[string]map[string]string{
	"en": {
		"setupTitle":      "Setting up {b}",
		"updTitle":        "Updating {b}",
		"startTitle":      "Starting {b}",
		"failSetupTitle":  "{b} could not be set up",
		"failTitle":       "{b} could not be updated",
		"stepDb":          "Prepare the database",
		"stepInstall":     "Install modules",
		"stepStart":       "Start {b}",
		"stepDownload":    "Download the new version",
		"stepUpdate":      "Update the database",
		"firstNote":       "The first setup takes a few minutes. This page opens {b} by itself.",
		"autoNote":        "This page opens {b} by itself.",
		"lastNote":        "The last update took {d}. This page opens {b} by itself.",
		"almostNote":      "Almost there. This page opens {b} by itself.",
		"slowNote":        "This is taking longer than last time ({d}). It is still working, so there's no need to reload.",
		"failNote":        "Please contact your administrator.",
		"failRetry":       "This page checks again by itself once someone retries.",
		"unavailableNote": "The status is unavailable right now. This page keeps checking.",
		"version":         "Version",
		"started":         "Started",
		"minutes":         "min",
		"seconds":         "s",
	},
	"ar": {
		"setupTitle":      "جاري تجهيز {b}",
		"updTitle":        "جاري تحديث {b}",
		"startTitle":      "جاري تشغيل {b}",
		"failSetupTitle":  "تعذّر تجهيز {b}",
		"failTitle":       "تعذّر تحديث {b}",
		"stepDb":          "تجهيز قاعدة البيانات",
		"stepInstall":     "تثبيت الوحدات",
		"stepStart":       "تشغيل {b}",
		"stepDownload":    "تنزيل الإصدار الجديد",
		"stepUpdate":      "تحديث قاعدة البيانات",
		"firstNote":       "يستغرق التجهيز الأول بضع دقائق. ستفتح هذه الصفحة {b} تلقائياً.",
		"autoNote":        "ستفتح هذه الصفحة {b} تلقائياً.",
		"lastNote":        "استغرق التحديث السابق {d}. ستفتح هذه الصفحة {b} تلقائياً.",
		"almostNote":      "أوشك على الانتهاء. ستفتح هذه الصفحة {b} تلقائياً.",
		"slowNote":        "يستغرق هذا وقتاً أطول من المرة السابقة ({d}). العمل مستمر ولا حاجة لإعادة تحميل الصفحة.",
		"failNote":        "يرجى التواصل مع المسؤول.",
		"failRetry":       "ستتحقق هذه الصفحة تلقائياً عند إعادة المحاولة.",
		"unavailableNote": "الحالة غير متاحة حالياً. تواصل هذه الصفحة التحقق.",
		"version":         "الإصدار",
		"started":         "بدأ في",
		"minutes":         "د",
		"seconds":         "ث",
	},
}

// pickLanguage returns "ar" or "en" from an Accept-Language header: the first
// listed language the page speaks, honouring q=0, defaulting to English.
func pickLanguage(acceptLanguage string) string {
	best, bestQ := "en", -1.0
	for _, part := range strings.Split(acceptLanguage, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		primary, _, _ := strings.Cut(tag, "-")
		primary, _, _ = strings.Cut(primary, "_")
		if _, ok := messages[primary]; !ok {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(param), "q="); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					q = f
				}
			}
		}
		if q > 0 && q > bestQ {
			best, bestQ = primary, q
		}
	}
	return best
}

// localized returns the messages of lang with {b} replaced by title.
func localized(lang, title string) map[string]string {
	out := make(map[string]string, len(messages[lang]))
	for k, v := range messages[lang] {
		out[k] = strings.ReplaceAll(v, "{b}", title)
	}
	return out
}
