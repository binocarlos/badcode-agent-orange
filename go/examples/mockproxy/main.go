package main

import (
	"log"
	"net/http"
	"os"

	"github.com/binocarlos/badcode-agent-orange/modelproxy"
)

func main() {
	addr := os.Getenv("MOCK_ADDR")
	if addr == "" {
		addr = ":4000"
	}
	log.Printf("[mockproxy] listening on %s (delegating to modelproxy.MockHandler)", addr)
	if err := http.ListenAndServe(addr, modelproxy.MockHandler()); err != nil {
		log.Fatalf("mockproxy: %v", err)
	}
}
