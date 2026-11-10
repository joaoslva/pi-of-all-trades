package main

import (
	crand "crypto/rand"
	"encoding/json"
	"flag"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func loadCharacters(path string) ([]Character, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var chars []Character
	return chars, json.Unmarshal(b, &chars)
}

func main() {
	listen := flag.String("listen", ":8090", "listen address")
	data := flag.String("data", "/var/lib/guessing-day", "data directory")
	web := flag.String("web", "web", "static files directory")
	content := flag.String("content", "content", "content directory")
	flag.Parse()

	chars, err := loadCharacters(filepath.Join(*content, "characters.json"))
	if err != nil {
		log.Fatal(err)
	}
	var seed [32]byte
	if _, err := crand.Read(seed[:]); err != nil {
		log.Fatal(err)
	}
	s, err := NewServer(NewGame(chars, rand.New(rand.NewChaCha8(seed))), *data, *content, *web)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
