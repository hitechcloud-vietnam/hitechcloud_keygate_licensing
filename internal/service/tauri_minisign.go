package service

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Tauri's updater verifies releases with minisign. Both values it is
// given are base64 encoded text files, decoded and parsed as such:
//
//	pubkey (tauri.conf.json):
//	  untrusted comment: minisign public key: <KEY ID>
//	  base64("Ed" + 8-byte key id + 32-byte public key)
//
//	signature (update manifest):
//	  untrusted comment: <anything>
//	  base64("Ed" + 8-byte key id + 64-byte signature of the file)
//	  trusted comment: <text>
//	  base64(64-byte signature of signature + trusted comment text)
//
// "Ed" is plain Ed25519 over the file bytes, which is exactly what
// Ed25519Sig already is; the trusted comment and global signature are
// the extra parts that need the private key.

// TauriPublicKey wraps a raw base64 Ed25519 public key in the form
// tauri.conf.json's updater pubkey expects. Returns "" for input that
// is not a 32-byte key.
func TauriPublicKey(rawPubKeyB64 string) string {
	rawPub, err := base64.StdEncoding.DecodeString(rawPubKeyB64)
	if err != nil || len(rawPub) != ed25519.PublicKeySize {
		return ""
	}
	keyID := tauriKeyID(rawPub)
	line := base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID[:]...), rawPub...))
	text := "untrusted comment: minisign public key: " + minisignKeyIDHex(keyID) + "\n" + line + "\n"
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// TauriTrustedComment is the trusted comment signed into a Tauri
// signature. It follows the Tauri CLI's "timestamp:...\tfile:..." and
// adds "version:", which Tauri compares with the version the manifest
// announces, so a signature cannot be replayed under another version.
func TauriTrustedComment(signedAt time.Time, filename, version string) string {
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	return fmt.Sprintf("timestamp:%d\tfile:%s\tversion:%s",
		signedAt.Unix(), clean.Replace(filename), clean.Replace(version))
}

// TauriSignature builds the base64 minisign signature file for a file
// whose plain Ed25519 signature is sig, signed by priv. Returns "" when
// sig is not a 64-byte signature.
func TauriSignature(priv ed25519.PrivateKey, sig []byte, trustedComment string) string {
	if len(sig) != ed25519.SignatureSize || len(priv) != ed25519.PrivateKeySize {
		return ""
	}
	keyID := tauriKeyID(priv.Public().(ed25519.PublicKey))
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), trustedComment...))
	text := "untrusted comment: signature from keygate\n" +
		base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID[:]...), sig...)) + "\n" +
		"trusted comment: " + trustedComment + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// tauriKeyID derives the 8-byte minisign key id from the public key.
// minisign picks random ids, but verifiers only compare the signature's
// id with the key's, so a stable derivation works and needs no column.
func tauriKeyID(rawPub []byte) [8]byte {
	h := sha256.Sum256(rawPub)
	var id [8]byte
	copy(id[:], h[:8])
	return id
}

// minisignKeyIDHex prints a key id the way minisign does: the bytes as a
// little endian number in upper case hex.
func minisignKeyIDHex(id [8]byte) string {
	var b strings.Builder
	for i := len(id) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "%02X", id[i])
	}
	return b.String()
}
