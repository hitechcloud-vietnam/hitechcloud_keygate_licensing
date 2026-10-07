package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanUploadFilename(t *testing.T) {
	cases := map[string]string{
		"My.Application-1.5+33-macOS.zip":   "My.Application-1.5+33-macOS.zip",
		"  spaced name.dmg  ":               "spaced name.dmg",
		"/home/user/builds/app.AppImage":    "app.AppImage",
		`C:\Users\me\Desktop\Setup 1.5.exe`: "Setup 1.5.exe",
		"../../etc/passwd":                  "passwd",
		"tab\there\x00nul\x1b.zip":          "tabherenul.zip",
		"中文名称-1.0.zip":                      "中文名称-1.0.zip",
		"setup\u202Egpj.exe":                "setupgpj.exe",
		"..":                                "",
		"dir/":                              "",
		"":                                  "",
	}
	for in, want := range cases {
		if got := cleanUploadFilename(in); got != want {
			t.Errorf("cleanUploadFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// Over-long names are cut to the byte cap without splitting a character.
func TestCleanUploadFilenameTruncatesOnRuneBoundary(t *testing.T) {
	got := cleanUploadFilename(strings.Repeat("名", 200) + ".zip") // 600+ bytes
	if len(got) > maxUploadFilenameBytes {
		t.Fatalf("len %d exceeds cap %d", len(got), maxUploadFilenameBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a multi-byte character")
	}
}
