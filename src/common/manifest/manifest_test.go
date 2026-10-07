package manifest

import "testing"

func TestIsSystemShell(t *testing.T) {
	for p, want := range map[string]bool{
		"/bin/sh":                             true,
		`C:\Windows\System32\cmd.exe`:         true,
		`c:\WINDOWS\system32\CMD.EXE`:         true,
		`D:\Windows\System32\cmd.exe`:         true,
		`C:\Windows\System32\calc.exe`:        false,
		`C:\Users\x\Windows\System32\cmd.exe`: false,
		"/tmp/sh":                             false,
	} {
		if IsSystemShell(p) != want {
			t.Errorf("%s: %v", p, !want)
		}
	}
}
