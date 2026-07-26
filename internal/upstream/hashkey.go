package upstream

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
	"github.com/cespare/xxhash/v2"
)

type compiledHashSource struct {
	kind  model.HashSourceType
	name  string
	value string
}

type hashKeyExtractor struct {
	sources []compiledHashSource
}

func compileHashKey(policy model.HashKeyPolicy) (hashKeyExtractor, error) {
	if len(policy.Sources) == 0 || len(policy.Sources) > MaxHashKeySources {
		return hashKeyExtractor{}, configError(
			"HASH_KEY_INVALID",
			"hash_key.sources",
			"",
			fmt.Errorf("requires 1..%d sources", MaxHashKeySources),
		)
	}
	extractor := hashKeyExtractor{
		sources: make([]compiledHashSource, len(policy.Sources)),
	}
	for index, source := range policy.Sources {
		source.Name = strings.Clone(source.Name)
		source.Value = strings.Clone(source.Value)
		if err := normalizeHashSource(&source, fmt.Sprintf("hash_key.sources[%d]", index), ""); err != nil {
			return hashKeyExtractor{}, err
		}
		extractor.sources[index] = compiledHashSource{
			kind:  source.Type,
			name:  source.Name,
			value: source.Value,
		}
	}
	return extractor, nil
}

func (e hashKeyExtractor) sum64(request *http.Request) (uint64, bool) {
	var digest xxhash.Digest
	digest.Reset()
	writeHashUvarint(&digest, uint64(len(e.sources)))

	anyPresent := false
	for _, source := range e.sources {
		writeHashByte(&digest, hashSourceTag(source.kind))
		switch source.kind {
		case model.HashSourceHeader:
			var values []string
			if request != nil {
				values = request.Header[source.name]
			}
			if len(values) == 0 {
				writeMissingHashComponent(&digest)
				continue
			}
			anyPresent = true
			writeHashByte(&digest, 1)
			writeHashUvarint(&digest, uint64(len(values)))
			for _, value := range values {
				writeHashString(&digest, value)
			}
		case model.HashSourceCookie:
			var (
				value string
				found bool
			)
			if request != nil {
				value, found = firstValidCookie(request.Header["Cookie"], source.name)
			}
			if !found {
				writeMissingHashComponent(&digest)
				continue
			}
			anyPresent = true
			writeHashByte(&digest, 1)
			writeHashUvarint(&digest, 1)
			writeHashString(&digest, value)
		case model.HashSourceRemoteAddr:
			if request == nil || request.RemoteAddr == "" {
				writeMissingHashComponent(&digest)
				continue
			}
			anyPresent = true
			writeHashByte(&digest, 1)
			writeHashUvarint(&digest, 1)
			writeHashRemoteAddr(&digest, request.RemoteAddr)
		case model.HashSourceLiteral:
			anyPresent = true
			writeHashByte(&digest, 1)
			writeHashUvarint(&digest, 1)
			writeHashString(&digest, source.value)
		}
	}
	if anyPresent {
		return digest.Sum64(), false
	}

	digest.Reset()
	writeHashUvarint(&digest, 1)
	writeHashByte(&digest, hashSourceTag(model.HashSourceRemoteAddr))
	writeHashByte(&digest, 1)
	writeHashUvarint(&digest, 1)
	if request == nil {
		writeHashString(&digest, "")
	} else {
		writeHashRemoteAddr(&digest, request.RemoteAddr)
	}
	return digest.Sum64(), true
}

func writeMissingHashComponent(digest *xxhash.Digest) {
	writeHashByte(digest, 0)
	writeHashUvarint(digest, 0)
}

func writeHashRemoteAddr(digest *xxhash.Digest, remoteAddr string) {
	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		writeHashString(digest, remoteAddr)
		return
	}
	var buffer [64]byte
	normalized := address.Unmap().AppendTo(buffer[:0])
	writeHashBytes(digest, normalized)
}

func writeHashString(digest *xxhash.Digest, value string) {
	writeHashUvarint(digest, uint64(len(value)))
	_, _ = digest.WriteString(value)
}

func writeHashBytes(digest *xxhash.Digest, value []byte) {
	writeHashUvarint(digest, uint64(len(value)))
	_, _ = digest.Write(value)
}

func writeHashUvarint(digest *xxhash.Digest, value uint64) {
	var buffer [10]byte
	length := binary.PutUvarint(buffer[:], value)
	_, _ = digest.Write(buffer[:length])
}

func writeHashByte(digest *xxhash.Digest, value byte) {
	var buffer [1]byte
	buffer[0] = value
	_, _ = digest.Write(buffer[:])
}

func hashSourceTag(sourceType model.HashSourceType) byte {
	switch sourceType {
	case model.HashSourceHeader:
		return 1
	case model.HashSourceCookie:
		return 2
	case model.HashSourceRemoteAddr:
		return 3
	case model.HashSourceLiteral:
		return 4
	default:
		return 0
	}
}

func firstValidCookie(headers []string, target string) (string, bool) {
	for _, header := range headers {
		remaining := header
		for {
			segment := remaining
			if separator := strings.IndexByte(remaining, ';'); separator >= 0 {
				segment = remaining[:separator]
				remaining = remaining[separator+1:]
			} else {
				remaining = ""
			}
			segment = strings.TrimSpace(segment)
			if separator := strings.IndexByte(segment, '='); separator > 0 && segment[:separator] == target {
				value := segment[separator+1:]
				if unquoted, valid := validCookieValue(value); valid {
					return unquoted, true
				}
			}
			if remaining == "" {
				break
			}
		}
	}
	return "", false
}

func validCookieValue(value string) (string, bool) {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character < 0x20 || character >= 0x7f || character == '"' || character == ';' || character == '\\' {
			return "", false
		}
	}
	return value, true
}
