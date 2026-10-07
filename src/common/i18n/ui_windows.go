package i18n

import (
	"syscall"
	"unsafe"
)

// uiLanguage is the Windows user's locale ("es-MX" -> "es").
func uiLanguage() string {
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName").Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	return Normalize(syscall.UTF16ToString(buf))
}

// keyboardLanguages lists the languages of the installed keyboard layouts (the low word of
// each HKL is a language id; its low 10 bits the primary language).
func keyboardLanguages() []string {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("GetKeyboardLayoutList")
	var hkl [64]uintptr
	n, _, _ := proc.Call(uintptr(len(hkl)), uintptr(unsafe.Pointer(&hkl[0])))
	var out []string
	for _, h := range hkl[:n] {
		switch h & 0x3ff {
		case 0x09:
			out = append(out, "en")
		case 0x0a:
			out = append(out, "es")
		case 0x0c:
			out = append(out, "fr")
		case 0x16:
			out = append(out, "pt")
		case 0x01:
			out = append(out, "ar")
		}
	}
	return out
}
