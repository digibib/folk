package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/cznic/ql"
	"github.com/gorilla/handlers"
	"github.com/knakk/ftx"
	"github.com/rcrowley/go-tigertonic"
	log "gopkg.in/inconshreveable/log15.v2"
)

// Global variables:
var (
	db             *ql.DB                                        // database handle
	cfg            *config                                       // configuration struct
	apiMux         *tigertonic.TrieServeMux                      // API router
	l              = log.New()                                   // logger
	imageFiles     []string                                      // list of uploaded images
	imageFileNames = regexp.MustCompile(`(\.png|\.jpg|\.jpeg)$`) // allowed image formats
	analyzer       *ftx.Analyzer                                 // indexing analyzer
)

const (
	MaxMemSize          = 2 * 1024 * 1024 // Maximum size of images to upload (2 MB)
	MaxPersonsLimit int = 200             // nr of Persons to fetch if limit is unset
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

// uploadHandler upload image files to the folder /data/img/
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(MaxMemSize); err != nil {
		log.Error("failed to parse multipart upload request", log.Ctx{"error": err.Error()})
		http.Error(w, err.Error(), http.StatusForbidden)
	}

	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			file, _ := fileHeader.Open()
			path := fmt.Sprintf("data/public/img/%s", fileHeader.Filename)
			// TODO check if filename allready exists
			buf, _ := ioutil.ReadAll(file)
			ioutil.WriteFile(path, buf, os.ModePerm)
		}
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

	// Index DB
	t0 := time.Now()
	analyzer = ftx.NewNGramAnalyzer(1, 20)
	ctx := ql.NewRWCtx()
	rs, _, err := db.Execute(ctx, qGetAllPersons, int64(0), int64(MaxPersonsLimit))
	if err != nil {
		log.Error("database query failed; exiting ", log.Ctx{"error": err.Error()})
		os.Exit(1)
	}

	var persons []*person
	for _, rs := range rs {
		if err := rs.Do(false, func(data []interface{}) (bool, error) {
			p := &person{}
			if err := ql.Unmarshal(p, data); err != nil {
				return false, err
			}
			persons = append(persons, p)
			return true, nil
		}); err != nil {
			log.Error("failed to unmarshal persons; exiting", log.Ctx{"error": err.Error()})
			os.Exit(1)
		}
	}

	for _, p := range persons {
		analyzer.Index(fmt.Sprintf("%v %v %v", p.Name, p.Role, p.Info), int(p.ID))
	}

	log.Info("Indexed DB", log.Ctx{"numPersons": len(persons), "took": time.Now().Sub(t0)})

	// Load list of images

	files, err := ioutil.ReadDir("./data/public/img/")
	if err != nil {
		log.Error("failed to read image directory", log.Ctx{"error": err.Error()})
	} else {
		for _, f := range files {
			if imageFileNames.MatchString(f.Name()) {
				imageFiles = append(imageFiles, f.Name())
			}
		}
	}

	// Request multiplexer

	mux := tigertonic.NewTrieServeMux()
	mux.HandleFunc("POST", "/upload", uploadHandler)

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

	l.Info("starting application", log.Ctx{"ServePort": cfg.ServePort})

	server := tigertonic.NewServer(fmt.Sprintf(":%d", cfg.ServePort), handlers.CompressHandler(mux))

	err = server.ListenAndServe()
	if err != nil {
		l.Error(err.Error())
	}
}
