package main

import (
	"bufio"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

//go:embed web/index.html
var webFS embed.FS

//go:embed web/admin.html
var adminHTML []byte

const defaultHashFile = "/etc/pihub/admin.hash"

var indexTmpl = template.Must(template.ParseFS(webFS, "web/index.html"))

type linkView struct {
	Service
	Href template.URL
}

// newHandler serves the landing page. Buttons are plain redirects to each
// service's own port on whatever host the browser used to reach us.
func newHandler(services []Service, auth *Auth) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", auth.adminPage)
	mux.HandleFunc("POST /admin/api/login", auth.login)
	mux.HandleFunc("GET /admin/api/stats", auth.require(auth.stats))
	mux.HandleFunc("POST /admin/api/power", auth.require(auth.power))
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

// passwd implements `pihub passwd`: read a password from stdin, store its hash.
func passwd(args []string) error {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	file := fs.String("file", defaultHashFile, "hash file to write")
	group := fs.String("group", "joao", "group that may read the hash file")
	fs.Parse(args)
	tty := exec.Command("stty", "-echo")
	tty.Stdin = os.Stdin
	hidden := tty.Run() == nil // fails when stdin is a pipe, which is fine
	if hidden {
		defer func() {
			restore := exec.Command("stty", "echo")
			restore.Stdin = os.Stdin
			restore.Run()
			fmt.Fprintln(os.Stderr)
		}()
		fmt.Fprint(os.Stderr, "New admin password: ")
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return setPassword(*file, *group, strings.TrimRight(line, "\r\n"))
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		if err := passwd(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	listen := flag.String("listen", ":80", "address to listen on")
	dir := flag.String("services-dir", "/etc/pihub/services.d", "directory of service JSON files")
	hash := flag.String("hash-file", defaultHashFile, "admin password hash file")
	flag.Parse()
	services, err := LoadServices(*dir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("pihub: %d service(s), listening on %s", len(services), *listen)
	log.Fatal(http.ListenAndServe(*listen, newHandler(services, NewAuth(*hash))))
}
