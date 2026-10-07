package orca

import (
	"strings"
	"testing"
)

// An input with an unknown keyword: ORCA prints "INPUT ERROR" and what is wrong, and no
// other error line (the client reported "ORCA ended without an error message (exit 4)").
func TestInputErrorText(t *testing.T) {
	out := `
                                 INPUT ERROR
            UNRECOGNIZED OR DUPLICATED KEYWORD(S) IN SIMPLE INPUT LINE
              NOTAKEYWORD          
            !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
[file orca_main/main_input_keywordline.cpp, line 13872]: 
`
	info, err := ParseOutput(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	want := "INPUT ERROR: UNRECOGNIZED OR DUPLICATED KEYWORD(S) IN SIMPLE INPUT LINE NOTAKEYWORD"
	if !info.ErrorTerm || info.ErrorText != want {
		t.Fatalf("error %v %q, want %q", info.ErrorTerm, info.ErrorText, want)
	}
}

// ORCA's input parser error (exit 126, nothing else printed): its message and the line.
func TestParserErrorText(t *testing.T) {
	out := "NOTE: MaxCore=768 MB was set to SCF\n      => If you want ...\nERROR: expect a '$', '!', '%', '*' or '[' in the input\n       Line 4 of job01.inp (C)\n"
	info, err := ParseOutput(strings.NewReader(out))
	if err != nil || !info.ErrorTerm || info.ErrorText != "ERROR: expect a '$', '!', '%', '*' or '[' in the input Line 4 of job01.inp (C)" {
		t.Errorf("error text %q (term %v)", info.ErrorText, info.ErrorTerm)
	}
}
