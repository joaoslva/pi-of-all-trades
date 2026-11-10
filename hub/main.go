package main

import (
	"embed"
	"flag"
	"html/template"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
)

//go:embed web/index.html
var webFS embed.FS

var indexTmpl = template.Must(template.ParseFS(webFS, "web/index.html"))

type linkView struct {
	Service
	Href template.URL
}

// newHandler serves the landing page. Buttons are plain redirects to each
// service's own port on whatever host the browser used to reach us.
func newHandler(services []Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = strings.Trim(r.Host, "[]") // no port in the Host header
		}
		views := make([]linkView, len(services))
		for i, s := range services {
			views[i] = linkView{s, template.URL("http://" + net.JoinHostPort(host, strconv.Itoa(s.Port)) + "/")}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexTmpl.Execute(w, views); err != nil {
			log.Print(err)
		}
	})
	return mux
}

func main() {
	listen := flag.String("listen", ":80", "address to listen on")
	dir := flag.String("services-dir", "/etc/pihub/services.d", "directory of service JSON files")
	flag.Parse()
	services, err := LoadServices(*dir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("pihub: %d service(s), listening on %s", len(services), *listen)
	log.Fatal(http.ListenAndServe(*listen, newHandler(services)))
}
