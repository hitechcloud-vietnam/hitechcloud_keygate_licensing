package service

import (
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/branding"
)

// The attribution footer (AGPL v3 Section 7(b)) is present on every
// outgoing body, whatever the body says: a mention of the domain — a
// download link on it — must not stand in for the footer.
func TestWithAttribution(t *testing.T) {
	footer := branding.EmailFooter
	s := newTemplateTestService()
	_, linked := s.RenderLicenseCreated("Acme", "Pro", "KG-1", "https://"+branding.Domain+"/download")

	for name, body := range map[string]string{
		"download link on the attribution domain": linked,
		"domain in plain text":                    "<html><body>see " + branding.Domain + "</body></html>",
		"no </body>":                              "<p>Hello</p>",
		"upper-case </BODY>":                      "<HTML><BODY>Hi</BODY></HTML>",
		"non-ASCII before </body>":                "<html><body>İstanbul ﬀ ẞ</body></html>",
	} {
		out := withAttribution(body)
		if strings.Count(out, footer) != 1 {
			t.Errorf("%s: footer appears %d times", name, strings.Count(out, footer))
			continue
		}
		// It sits inside the body when there is one.
		if i := lastIndexASCIIFold(out, "</body>"); i >= 0 && !strings.HasSuffix(out[:i], footer) {
			t.Errorf("%s: footer not placed before </body>:\n%s", name, out)
		}
	}

	// A custom template that carries the footer where nobody sees it
	// still gets the visible one, and only that one.
	hidden := withAttribution(`<html><body><div style="display:none">` + footer + `</div>Hi</body></html>`)
	if strings.Count(hidden, footer) != 1 || strings.Contains(hidden, `display:none">`+footer) {
		t.Errorf("hidden copy of the footer was kept:\n%s", hidden)
	}

	// Already carrying the footer: left as it is, never doubled.
	once := withAttribution("<html><body>x</body></html>")
	if withAttribution(once) != once {
		t.Error("a body that already has the footer was changed")
	}
}
