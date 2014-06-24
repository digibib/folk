package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/cznic/ql"
	"github.com/rcrowley/go-tigertonic"
	log "gopkg.in/inconshreveable/log15.v2"
)

// Global variables:
var (
	db     *ql.DB                   // database handle
	cfg    *config                  // configuration struct
	apiMux *tigertonic.TrieServeMux // API router
	l      = log.New()              // logger
)

type config struct {
	ServePort int    // HTTP port to serve from
	LogFile   string // path to log file
	DBFile    string // path to database file
	Username  string // basic auth username
	Password  string // basic auth password
}

// serveFile serves a single file from disk.
func serveFile(filename string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filename)
	}
}

func main() {
	// Configuration defaults
	cfg = &config{
		ServePort: 9999,
		DBFile:    "data/folk.db",
		LogFile:   "folk.log",
	}

	// Log to both Stdout and file
	l.SetHandler(log.MultiHandler(
		log.LvlFilterHandler(log.LvlInfo, log.Must.FileHandler(cfg.LogFile, log.LogfmtFormat())),
		log.StreamHandler(os.Stdout, log.TerminalFormat())),
	)

	// Trap ^C to make sure we close the database before exiting; this is the
	// only way to make sure all commits are actually flushed to disk.
	interruptChan := make(chan os.Signal, 1)
	signal.Notify(interruptChan, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	go func() {
		<-interruptChan
		l.Info("interrupt signal received; exiting")

		err := db.Close()
		if err != nil {
			l.Error("db.Close() failed", log.Ctx{"error": err})
		}
		os.Exit(0)
	}()

	// Init DB handle
	var err error
	db, err = ql.OpenFile(cfg.DBFile, &ql.Options{CanCreate: true})
	if err != nil {
		log.Error("failed to init DB; exiting", log.Ctx{"error": err.Error(), "file": cfg.LogFile})
		os.Exit(0)
	}
	err = createSchema(db)
	if err != nil {
		log.Error("failed to create DB schema; exiting", log.Ctx{"error": err.Error(), "file": cfg.LogFile})
		db.Close()
		os.Exit(0)
	}

	mux := tigertonic.NewTrieServeMux()

	// Static assets
	mux.HandleNamespace("/public", http.FileServer(http.Dir("data/public/")))
	mux.HandleFunc("GET", "/robots.txt", serveFile("data/robots.txt"))

	// Public pages
	mux.HandleFunc("GET", "/", serveFile("data/html/public.html"))
	mux.HandleFunc("GET", "/admin", serveFile("data/html/admin.html"))

	// API routing
	setupAPIRouting()
	mux.HandleNamespace("/api", apiMux)

	tigertonic.SnakeCaseHTTPEquivErrors = true

	l.Info("starting application")

	server := tigertonic.NewServer(fmt.Sprintf(":%d", cfg.ServePort), mux)

	err = server.ListenAndServe()
	if err != nil {
		l.Error(err.Error())
	}
}
