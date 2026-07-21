package amazon

//nolint:unused // all functions called cross-file from community.go

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
)

// decryptWithMP4FF decrypts an encrypted fragmented MP4 using per-KID keys.
//
// keySpecs supports two formats:
//   - Legacy:   ["hexkey"]                          → single global key
//   - Strict:   ["KID_HEX:KEY_HEX", ...]            → per-KID key map
//
// The two formats are mutually exclusive within a single call.
func decryptWithMP4FF(keySpecs []string, inputPath, outputPath string) error {
	key, keysByKID, strictKIDMode, err := parseKeySpecs(keySpecs)
	if err != nil {
		return err
	}

	inFile, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("mp4ff: open encrypted file: %w", err)
	}
	defer inFile.Close()

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("mp4ff: create decrypted file: %w", err)
	}
	outClosed := false
	defer func() {
		if !outClosed {
			outFile.Close()
		}
	}()

	if err := decryptFile(inFile, nil, outFile, key, keysByKID, strictKIDMode); err != nil {
		outFile.Close()
		outClosed = true
		os.Remove(outputPath)
		return fmt.Errorf("mp4ff: decryption failed: %w", err)
	}

	if err := outFile.Close(); err != nil {
		outClosed = true
		os.Remove(outputPath)
		return fmt.Errorf("mp4ff: finalize output: %w", err)
	}
	outClosed = true
	return nil
}

// parseKeySpecs normalises and classifies a key spec list.
//
//nolint:unused
func parseKeySpecs(keySpecs []string) (key []byte, keysByKID map[string][]byte, strict bool, err error) {
	normalized := make([]string, 0, len(keySpecs))
	seen := make(map[string]struct{}, len(keySpecs))
	for _, spec := range keySpecs {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		if _, ok := seen[spec]; ok {
			continue
		}
		seen[spec] = struct{}{}
		normalized = append(normalized, spec)
	}

	if len(normalized) == 0 {
		return nil, nil, false, fmt.Errorf("mp4ff: no key specs provided")
	}

	hasKID := false
	hasLegacy := false
	for _, spec := range normalized {
		if strings.Contains(spec, ":") {
			hasKID = true
		} else {
			hasLegacy = true
		}
	}
	if hasKID && hasLegacy {
		return nil, nil, false, fmt.Errorf("mp4ff: mixed KID:KEY and legacy key formats")
	}

	if !hasKID {
		if len(normalized) != 1 {
			return nil, nil, false, fmt.Errorf("mp4ff: multiple legacy keys unsupported")
		}
		key, err = mp4.UnpackKey(normalized[0])
		if err != nil {
			return nil, nil, false, fmt.Errorf("mp4ff: unpack key: %w", err)
		}
		return key, nil, false, nil
	}

	keysByKID = make(map[string][]byte, len(normalized))
	for _, spec := range normalized {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, nil, false, fmt.Errorf("mp4ff: bad KID:KEY format %q", spec)
		}
		kidRaw := strings.TrimSpace(parts[0])
		keyRaw := strings.TrimSpace(parts[1])

		kid, err := mp4.UnpackKey(kidRaw)
		if err != nil {
			return nil, nil, false, fmt.Errorf("mp4ff: unpack KID %s: %w", kidRaw, err)
		}
		kidHex := hex.EncodeToString(kid)
		if _, exists := keysByKID[kidHex]; exists {
			return nil, nil, false, fmt.Errorf("mp4ff: duplicate KID %s", kidHex)
		}
		k, err := mp4.UnpackKey(keyRaw)
		if err != nil {
			return nil, nil, false, fmt.Errorf("mp4ff: unpack key for KID %s: %w", kidHex, err)
		}
		keysByKID[kidHex] = k
	}
	return nil, keysByKID, true, nil
}

// decryptFile reads a fragmented MP4, decrypts it, and writes the result.
//
// initR is an optional separate init segment. When the file contains its own
// init (moov box), initR is ignored.
//
//nolint:unused
func decryptFile(r io.Reader, initR io.Reader, w io.Writer,
	key []byte, keysByKID map[string][]byte, strictKIDMode bool,
) error {
	inMp4, err := mp4.DecodeFile(r)
	if err != nil {
		return fmt.Errorf("mp4ff: decode: %w", err)
	}
	if !inMp4.IsFragmented() {
		return fmt.Errorf("mp4ff: file is not fragmented — unsupported")
	}

	init := inMp4.Init
	if init == nil {
		if initR == nil {
			return fmt.Errorf("mp4ff: no init segment in file and none provided")
		}
		initSegment, err := mp4.DecodeFile(initR)
		if err != nil {
			return fmt.Errorf("mp4ff: decode init segment: %w", err)
		}
		init = initSegment.Init
	}

	decryptInfo, err := mp4.DecryptInit(init)
	if err != nil {
		return fmt.Errorf("mp4ff: decrypt init: %w", err)
	}

	// Write init segment first.
	if inMp4.Init != nil {
		if err := inMp4.Init.Encode(w); err != nil {
			return fmt.Errorf("mp4ff: encode init: %w", err)
		}
	}

	for _, seg := range inMp4.Segments {
		// Parse senc from init if the file didn't include its own init.
		if inMp4.Init == nil {
			if err := seg.ParseSenc(init); err != nil {
				return fmt.Errorf("mp4ff: parse senc: %w", err)
			}
		}
		if err := decryptSegment(seg, decryptInfo, key, keysByKID, strictKIDMode); err != nil {
			return fmt.Errorf("mp4ff: decrypt segment: %w", err)
		}
		if err := seg.Encode(w); err != nil {
			return fmt.Errorf("mp4ff: encode segment: %w", err)
		}
	}
	return nil
}

// decryptSegment decrypts every fragment in a segment that carries a senc box.
//
//nolint:unused
func decryptSegment(seg *mp4.MediaSegment, info mp4.DecryptInfo,
	key []byte, keysByKID map[string][]byte, strictKIDMode bool,
) error {
	for _, frag := range seg.Fragments {
		if !fragmentHasSenc(frag) {
			continue
		}
		if err := mp4.DecryptFragmentWithKeys(frag, info, key, keysByKID, strictKIDMode); err != nil {
			return err
		}
	}
	// Remove sidx boxes — they become stale after decryption.
	if len(seg.Sidxs) > 0 {
		seg.Sidx = nil
		seg.Sidxs = nil
	}
	return nil
}

//nolint:unused
func fragmentHasSenc(frag *mp4.Fragment) bool {
	if frag == nil || frag.Moof == nil {
		return false
	}
	for _, traf := range frag.Moof.Trafs {
		if traf == nil {
			continue
		}
		if has, _ := traf.ContainsSencBox(); has {
			return true
		}
	}
	return false
}
