package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// tauriVerify is a port of tauri-plugin-updater's verify_signature and
// the minisign-verify crate it calls: both values are base64 decoded to
// UTF-8 text, the public key is two lines and the signature four, the
// key ids must match, the file signature and the global signature over
// signature + trusted comment must verify, and a "version:" field in
// the trusted comment must equal the announced version.
func tauriVerify(data []byte, signatureB64, pubkeyB64, announced string) error {
	text := func(b64 string) ([]string, error) {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("base64: %w", err)
		}
		// Rust's str::lines: split on \n, drop a trailing empty line.
		lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
		return lines, nil
	}
	pubLines, err := text(pubkeyB64)
	if err != nil || len(pubLines) < 2 {
		return fmt.Errorf("pubkey: invalid encoding (%v)", err)
	}
	pubBin, err := base64.StdEncoding.DecodeString(pubLines[1])
	if err != nil || len(pubBin) != 42 || string(pubBin[0:2]) != "Ed" && string(pubBin[0:2]) != "ED" {
		return errors.New("pubkey: invalid key line")
	}
	sigLines, err := text(signatureB64)
	if err != nil || len(sigLines) < 4 {
		return fmt.Errorf("signature: invalid encoding (%v)", err)
	}
	bin1, err := base64.StdEncoding.DecodeString(sigLines[1])
	if err != nil || len(bin1) != 74 {
		return errors.New("signature: invalid signature line")
	}
	trusted := sigLines[2]
	bin2, err := base64.StdEncoding.DecodeString(sigLines[3])
	if err != nil || len(bin2) != 64 || !strings.HasPrefix(trusted, "trusted comment: ") {
		return errors.New("signature: invalid trusted comment or global signature")
	}
	if string(bin1[0:2]) != "Ed" {
		return errors.New("signature: only legacy Ed signatures are produced")
	}
	if string(bin1[2:10]) != string(pubBin[2:10]) {
		return errors.New("unexpected key id")
	}
	pub := ed25519.PublicKey(pubBin[10:42])
	if !ed25519.Verify(pub, data, bin1[10:74]) {
		return errors.New("invalid signature")
	}
	comment := strings.TrimPrefix(trusted, "trusted comment: ")
	if !ed25519.Verify(pub, append(append([]byte{}, bin1[10:74]...), comment...), bin2) {
		return errors.New("invalid global signature")
	}
	for _, field := range strings.Split(comment, "\t") {
		if v, ok := strings.CutPrefix(field, "version:"); ok {
			if strings.TrimPrefix(v, "v") != strings.TrimPrefix(announced, "v") {
				return fmt.Errorf("signed version %s does not match %s", v, announced)
			}
		}
	}
	return nil
}

func tauriFixture(t *testing.T) (ed25519.PrivateKey, string, []byte, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("an update bundle")
	sig := ed25519.Sign(priv, data)
	comment := TauriTrustedComment(time.Unix(1791100000, 0), "app_1.2.3_x64.tar.gz", "1.2.3")
	return priv, TauriPublicKey(base64.StdEncoding.EncodeToString(pub)), data, TauriSignature(priv, sig, comment)
}

func TestTauriSignatureVerifies(t *testing.T) {
	_, pubkey, data, signature := tauriFixture(t)
	if err := tauriVerify(data, signature, pubkey, "1.2.3"); err != nil {
		t.Fatalf("Tauri would reject a valid signature: %v", err)
	}
	if err := tauriVerify(data, signature, pubkey, "v1.2.3"); err != nil {
		t.Fatalf("a leading v in the announced version must still match: %v", err)
	}
}

func TestTauriSignatureRejected(t *testing.T) {
	_, pubkey, data, signature := tauriFixture(t)
	if tauriVerify([]byte("tampered bundle"), signature, pubkey, "1.2.3") == nil {
		t.Error("a changed file must not verify")
	}
	if tauriVerify(data, signature, pubkey, "1.2.4") == nil {
		t.Error("a signature for 1.2.3 must not verify as 1.2.4")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if tauriVerify(data, signature, TauriPublicKey(base64.StdEncoding.EncodeToString(otherPub)), "1.2.3") == nil {
		t.Error("another key must not verify")
	}
	raw, _ := base64.StdEncoding.DecodeString(signature)
	forged := strings.Replace(string(raw), "version:1.2.3", "version:9.9.9", 1)
	if tauriVerify(data, base64.StdEncoding.EncodeToString([]byte(forged)), pubkey, "9.9.9") == nil {
		t.Error("an edited trusted comment must fail the global signature")
	}
}

func TestTauriPublicKeyIsMinisignText(t *testing.T) {
	_, pubkey, _, _ := tauriFixture(t)
	raw, err := base64.StdEncoding.DecodeString(pubkey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "untrusted comment: minisign public key: ") {
		t.Errorf("pubkey must decode to a minisign public key file, got %q", raw)
	}
	if got := TauriPublicKey("not a key"); got != "" {
		t.Errorf("bad input must give an empty key, got %q", got)
	}
}

func TestTauriSignatureRejectsBadInput(t *testing.T) {
	priv, _, _, _ := tauriFixture(t)
	if got := TauriSignature(priv, make([]byte, 32), "c"); got != "" {
		t.Errorf("a short signature must give an empty result, got %q", got)
	}
}

func TestTauriTrustedCommentStaysOnOneLine(t *testing.T) {
	c := TauriTrustedComment(time.Unix(1, 0), "evil\nname\twith\rbreaks", "1.0.0")
	if strings.ContainsAny(c, "\n\r") || strings.Count(c, "\t") != 2 {
		t.Errorf("trusted comment must be one line with three fields, got %q", c)
	}
}

func TestBuildTauriUsesStoredSignature(t *testing.T) {
	now := time.Now()
	in := FeedInput{Releases: []*FeedRelease{{
		Release:     &model.Release{Version: "1.2.3", PublishedAt: &now},
		Artifact:    &model.ReleaseArtifact{Ed25519Sig: "raw", TauriSignature: "stored"},
		DownloadURL: "https://example.com/app.tar.gz",
	}}}
	if got := BuildTauri(in).Signature; got != "stored" {
		t.Errorf("manifest must carry the stored Tauri signature, got %q", got)
	}
}
