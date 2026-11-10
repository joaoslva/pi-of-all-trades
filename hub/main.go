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
func newHandler(services []Service, auth *Auth, ctl *Control) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin", auth.adminPage)
	mux.HandleFunc("POST /admin/api/login", auth.login)
	mux.HandleFunc("GET /admin/api/stats", auth.require(auth.stats))
	mux.HandleFunc("POST /admin/api/power", auth.require(auth.power))
	if ctl != nil {
		mux.HandleFunc("GET /admin/api/services", auth.require(ctl.list))
		mux.HandleFunc("POST /admin/api/switch", auth.require(ctl.twoStep(auth, "switch", ctl.Switch)))
		mux.HandleFunc("POST /admin/api/stop", auth.require(ctl.twoStep(auth, "stop", ctl.Stop)))
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = strings.Trim(r.Host, "[]") // no port in the Host header
		}
		var views []linkView
		for _, s := range services {
			if s.Listed() {
				views = append(views, linkView{s, template.URL("http://" + net.JoinHostPort(host, strconv.Itoa(s.Port)) + "/")})
			}
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
	stateDir := flag.String("state-dir", "/var/lib/pihub", "directory for state.json")
	hash := flag.String("hash-file", defaultHashFile, "admin password hash file")
	flag.Parse()
	services, err := LoadServices(*dir)
	if err != nil {
		log.Fatal(err)
	}
	ctl := NewControl(systemctl{}, services, *stateDir)
	if err := ctl.Reconcile(); err != nil {
		log.Printf("reconcile: %v", err) // the hub still serves; the admin page shows what is running
	}
	log.Printf("pihub: %d service(s), listening on %s", len(services), *listen)
	log.Fatal(http.ListenAndServe(*listen, newHandler(services, NewAuth(*hash), ctl)))
}
