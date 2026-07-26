package upstream

import (
	"net/http"
	"testing"

	"github.com/QuanTuanHuy/g-gateway/internal/model"
)

func FuzzHashKey(f *testing.F) {
	for _, seed := range [][2]string{
		{"session_id=abc", "192.0.2.1:1234"},
		{"other=x; session_id=first", "[::1]:8080"},
		{`session_id="quoted"`, "invalid-peer"},
		{"", ""},
	} {
		f.Add(seed[0], seed[1])
	}
	extractor, err := compileHashKey(model.HashKeyPolicy{Sources: []model.HashKeySource{
		{Type: model.HashSourceCookie, Name: "session_id"},
		{Type: model.HashSourceRemoteAddr},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, rawCookie, remoteAddr string) {
		request := &http.Request{
			Header:     http.Header{"Cookie": {rawCookie}},
			RemoteAddr: remoteAddr,
		}
		_, _ = extractor.sum64(request)
	})
}
