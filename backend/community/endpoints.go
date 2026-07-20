// Package community talks to the shared provider infrastructure run by the
// upstream project (spotbye). It is the only path left to Qobuz and Amazon: as
// of 2026-07-20 every independent public proxy for those two is dead, while
// Tidal still works through its own unrelated ecosystem and needs none of this.
//
// Three things are worth knowing before reading further.
//
// The endpoint URLs are AES-GCM encrypted in upstream's source with the key
// derivable from a literal in the same file. That is a sign, not a lock — it
// says "this is my infrastructure, I pay for it". Decrypting them is a project
// decision that was taken deliberately (docs/upstream-catchup.md §S1), because
// we already hard-code one of these hosts and it moved.
//
// Access requires a session obtained through a human verification challenge.
// No automated solving of that challenge exists here and none will be added;
// the only supported flow is one where a person completes it themselves.
//
// The session lasts about 6 hours and is bound to the IP that solved the
// challenge (measured 2026-07-20). Both facts shape everything above this
// layer — see docs/external-api-layer.md.
package community

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"fmt"
	"sync"
)

// The seed is split across parts for the same reason upstream splits it: a
// plain grep for the string does not find it. Reassembled it reads
// "spotiflac:community:url:v1".
var urlSeedParts = [][]byte{
	[]byte("spotif"),
	[]byte("lac:co"),
	[]byte("mmunity:url:v1"),
}

var urlAAD = []byte("spotiflac|community|url|v1")

// Each service's base URL, as nonce + ciphertext + GCM tag. Verified to decrypt
// to live hosts on 2026-07-20 (see endpoints_test.go, which pins the results so
// a bad edit to these byte tables fails loudly instead of silently producing a
// wrong host).
var (
	tidalNonce      = []byte{0x67, 0xfc, 0xe8, 0xc2, 0x2e, 0x43, 0xef, 0x00, 0x03, 0x8e, 0xf7, 0x7c}
	tidalCiphertext = []byte{
		0xeb, 0x2e, 0x2e, 0x26, 0xbf, 0x49, 0x8f, 0xc7, 0x5e, 0x14, 0x6c, 0xfb,
		0xd2, 0x24, 0x07, 0xf0, 0x9d, 0x17, 0x55, 0x03, 0x1b, 0x09, 0x20, 0x31,
		0x71, 0xeb, 0xf8, 0x7c, 0x33, 0x7d,
	}
	tidalTag = []byte{
		0xa8, 0x67, 0xc6, 0x71, 0x4c, 0x5c, 0x2a, 0xfc, 0x4e, 0x83, 0xfc, 0x0b,
		0x36, 0xcc, 0x21, 0xe9,
	}

	qobuzNonce      = []byte{0x36, 0xf7, 0x2d, 0xdf, 0x93, 0xea, 0x36, 0x68, 0xb6, 0x66, 0xf0, 0x5a}
	qobuzCiphertext = []byte{
		0x56, 0x5d, 0x00, 0xd6, 0x0b, 0x39, 0x8a, 0x14, 0xd3, 0x88, 0x30, 0x04,
		0x58, 0x3d, 0x8f, 0x1b, 0x09, 0x87, 0x02, 0xb3, 0x37, 0xf7, 0x09, 0xd3,
		0xeb, 0x44, 0x72, 0x47, 0xc9, 0x44,
	}
	qobuzTag = []byte{
		0x40, 0x9f, 0xa0, 0xe8, 0x50, 0x4a, 0x7e, 0xee, 0x29, 0x7e, 0x29, 0x01,
		0x6b, 0x05, 0x3a, 0xdc,
	}

	amazonNonce      = []byte{0x3a, 0xb7, 0xd4, 0xd5, 0xd1, 0x7b, 0xbf, 0x11, 0x1d, 0x50, 0xfa, 0x81}
	amazonCiphertext = []byte{
		0x7a, 0x2b, 0x4e, 0x52, 0x98, 0x85, 0x24, 0xa9, 0x58, 0xb9, 0x85, 0x63,
		0xef, 0x8e, 0x5a, 0x01, 0x3c, 0xa4, 0xf5, 0x94, 0xe4, 0x68, 0x46, 0x19,
		0x06, 0x48, 0x32, 0xd8, 0xb8, 0xfa,
	}
	amazonTag = []byte{
		0x6e, 0x13, 0x44, 0x7b, 0x9e, 0x5e, 0x7f, 0x95, 0x57, 0xc7, 0x8e, 0x80,
		0x42, 0x8e, 0x76, 0x49,
	}

	verifyNonce      = []byte{0x37, 0x68, 0x07, 0x7e, 0xe1, 0x02, 0x94, 0xd7, 0x24, 0xd7, 0xdc, 0x54}
	verifyCiphertext = []byte{
		0x01, 0x6d, 0xb0, 0x5f, 0x66, 0x08, 0xab, 0x6a, 0x99, 0x66, 0x5b, 0xfc,
		0x70, 0x99, 0xe6, 0xdb, 0x54, 0xa7, 0x9e, 0x20, 0xb9, 0x6b, 0xd3, 0xca,
		0x42, 0xb4, 0xaf, 0xc5, 0x69,
	}
	verifyTag = []byte{
		0x1d, 0x91, 0x11, 0xce, 0xf7, 0xe2, 0x18, 0x76, 0xe0, 0x5d, 0xb3, 0xc5,
		0xee, 0x99, 0xe4, 0xf2,
	}
)

// downloadPath is appended to a service's base URL to reach its download API.
const downloadPath = "/api/dl"

var (
	aeadOnce sync.Once
	aead     cipher.AEAD
	aeadErr  error
)

func urlCipher() (cipher.AEAD, error) {
	aeadOnce.Do(func() {
		h := sha256.New()
		for _, part := range urlSeedParts {
			h.Write(part)
		}
		block, err := aes.NewCipher(h.Sum(nil))
		if err != nil {
			aeadErr = err
			return
		}
		aead, aeadErr = cipher.NewGCM(block)
	})
	return aead, aeadErr
}

// decryptURL returns the plaintext base URL for one service.
//
// Unlike upstream, the error is returned rather than swallowed. Upstream's
// getters discard it and return "" + downloadPath, so a decryption failure
// becomes a request to the path "/api/dl" with no host — an error that surfaces
// far from its cause.
func decryptURL(nonce, ciphertext, tag []byte) (string, error) {
	gcm, err := urlCipher()
	if err != nil {
		return "", fmt.Errorf("community: cipher init: %w", err)
	}
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plaintext, err := gcm.Open(nil, nonce, sealed, urlAAD)
	if err != nil {
		return "", fmt.Errorf("community: endpoint decryption failed: %w", err)
	}
	return string(plaintext), nil
}

// TidalDownloadURL returns the community Tidal download endpoint.
//
// Present for completeness; our Tidal path does not use it. Tidal works through
// the official API plus independent third-party proxies, which is exactly why
// it still works while Qobuz and Amazon do not.
func TidalDownloadURL() (string, error) {
	base, err := decryptURL(tidalNonce, tidalCiphertext, tidalTag)
	if err != nil {
		return "", err
	}
	return base + downloadPath, nil
}

// QobuzDownloadURL returns the community Qobuz download endpoint.
func QobuzDownloadURL() (string, error) {
	base, err := decryptURL(qobuzNonce, qobuzCiphertext, qobuzTag)
	if err != nil {
		return "", err
	}
	return base + downloadPath, nil
}

// QobuzHealthURL returns the community Qobuz health endpoint, which answers
// without a session and is therefore usable as a liveness probe.
func QobuzHealthURL() (string, error) {
	base, err := decryptURL(qobuzNonce, qobuzCiphertext, qobuzTag)
	if err != nil {
		return "", err
	}
	return base + "/health", nil
}

// AmazonDownloadURL returns the community Amazon download endpoint.
func AmazonDownloadURL() (string, error) {
	base, err := decryptURL(amazonNonce, amazonCiphertext, amazonTag)
	if err != nil {
		return "", err
	}
	return base + downloadPath, nil
}

// VerifyBaseURL returns the base URL of the human-verification service, which
// serves /bootstrap and /session/exchange.
func VerifyBaseURL() (string, error) {
	return decryptURL(verifyNonce, verifyCiphertext, verifyTag)
}
