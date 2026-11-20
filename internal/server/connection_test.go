package server

import (
	"encoding/json"
	"grimcraft-go/internal/hub"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebTransportDiscovery(testCase *testing.T) {
	info := &ConnectionInfo{Enabled: true, WebTransportURL: "https://127.0.0.1:4433/grimcraft", CertificateHash: strings.Repeat("a", 64)}
	handler := API(hub.NewHub(), info)
	for _, hash := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		info.CertificateHash = hash
		responseRecorder := httptest.NewRecorder()
		handler.ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/api/connection", nil))
		if responseRecorder.Code != http.StatusOK || responseRecorder.Header().Get("Cache-Control") != "no-store" {
			testCase.Fatal("discovery must be successful and uncached")
		}
		var actualResult ConnectionInfo
		if operationError := json.Unmarshal(responseRecorder.Body.Bytes(), &actualResult); operationError != nil {
			testCase.Fatal(operationError)
		}
		if actualResult != *info {
			testCase.Fatalf("wrong or stale metadata: %+v", actualResult)
		}
		if responseRecorder.Header().Get("Access-Control-Allow-Origin") != "*" {
			testCase.Fatal("local frontend cannot read discovery")
		}
	}
}
func TestDiscoveryWithoutWebTransport(testCase *testing.T) {
	responseRecorder := httptest.NewRecorder()
	API(hub.NewHub()).ServeHTTP(responseRecorder, httptest.NewRequest(http.MethodGet, "/api/connection", nil))
	var info ConnectionInfo
	if operationError := json.Unmarshal(responseRecorder.Body.Bytes(), &info); operationError != nil {
		testCase.Fatal(operationError)
	}
	if info.Enabled || info.CertificateHash != "" || info.WebTransportURL != "" {
		testCase.Fatal("disabled transport must not advertise a certificate")
	}
}

func TestLobbyCreationUsesKnownBattlefields(testCase *testing.T) {
	handler := API(hub.NewHub(), &ConnectionInfo{WebSocketPath: "/join"})
	for _, test := range []struct {
		body string
		want int
	}{
		{`{"name":"Network arena","map":"Default Arena","capacity":4}`, http.StatusCreated},
		{`{"name":"Network doom","map":"/maps/4p_doom_spiral/4p_doom_spiral.sgb","capacity":4}`, http.StatusCreated},
		{`{"name":"Too many players","map":"Doom Spiral","capacity":8}`, http.StatusBadRequest},
		{`{"name":"Network Dota","map":"Dota 2","capacity":2}`, http.StatusCreated},
		{`{"name":"Network Dota by path","map":"/maps/dotamap-game/map.json","capacity":2}`, http.StatusCreated},
		{`{"name":"Too many Dota players","map":"Dota 2","capacity":4}`, http.StatusBadRequest},
		{`{"name":"Unknown battlefield","map":"Nonexistent","capacity":2}`, http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/lobbies", strings.NewReader(test.body)))
		if response.Code != test.want {
			testCase.Fatalf("%s: got %d, want %d (%s)", test.body, response.Code, test.want, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/connection", nil))
	var info ConnectionInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil || info.Enabled || info.WebSocketPath != "/join" {
		testCase.Fatalf("invalid WebSocket-only discovery: %+v, %v", info, err)
	}
}
