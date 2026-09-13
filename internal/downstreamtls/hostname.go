package downstreamtls

import (
	"errors"
	"net/netip"
	"strings"
)

var errInvalidServerName = errors.New("invalid server name")

func normalizeBindingHost(raw string) (string, bool, error) {
	wildcard := strings.HasPrefix(raw, "*.")
	if wildcard {
		raw = raw[2:]
	}

	var scratch [253]byte
	canonical, err := canonicalizeServerName(raw, &scratch)
	if err != nil {
		return "", false, err
	}
	if wildcard && !strings.Contains(string(canonical), ".") {
		return "", false, errInvalidServerName
	}
	return string(canonical), wildcard, nil
}

func canonicalizeServerName(raw string, scratch *[253]byte) ([]byte, error) {
	if scratch == nil {
		return nil, errInvalidServerName
	}
	if strings.HasSuffix(raw, ".") {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 || len(raw) > len(scratch) {
		return nil, errInvalidServerName
	}

	labelStart := 0
	for index := range raw {
		character := raw[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if character == '.' {
			if index == labelStart || index-labelStart > 63 ||
				scratch[labelStart] == '-' || scratch[index-1] == '-' {
				return nil, errInvalidServerName
			}
			labelStart = index + 1
		} else if !isDNSLabelCharacter(character) {
			return nil, errInvalidServerName
		}
		scratch[index] = character
	}
	if len(raw)-labelStart == 0 || len(raw)-labelStart > 63 ||
		scratch[labelStart] == '-' || scratch[len(raw)-1] == '-' {
		return nil, errInvalidServerName
	}

	canonical := scratch[:len(raw)]
	if couldBeIPv4Literal(canonical) {
		if _, err := netip.ParseAddr(string(canonical)); err == nil {
			return nil, errInvalidServerName
		}
	}
	return canonical, nil
}

func couldBeIPv4Literal(canonical []byte) bool {
	for _, character := range canonical {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return true
}

func isDNSLabelCharacter(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= '0' && character <= '9') || character == '-'
}
