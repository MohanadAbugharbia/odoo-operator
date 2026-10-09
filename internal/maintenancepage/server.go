// Package maintenancepage serves the page the http Service points at while
// Odoo cannot answer (spec.maintenancePage). It runs from the operator's own
// image as `odoo-operator maintenance-page`.
//
// It answers the way a server that is down does, so that Odoo's own clients
// keep their offline behaviour: a page navigation gets the status page, with
// status 503; every other request (RPC, assets, /web/health, service worker
// fetches) gets an empty 503. Nothing is ever cacheable.
package maintenancepage

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"sigs.k8s.io/controller-runtime/pkg/log"

	odoov1 "github.com/MohanadAbugharbia/odoo-operator/api/v1"
)

// Command is the subcommand that runs the server.
const Command = "maintenance-page"

// retryAfter is sent with every 503. The page itself polls faster.
const retryAfter = "10"

//go:embed page.html.tmpl
var pageSource string

var pageTemplate = template.Must(template.New("page").Parse(pageSource))

var (
	accentPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	logoPattern   = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp|svg\+xml);base64,[A-Za-z0-9+/]+=*$`)
)

// contentSecurityPolicy allows nothing but the inline page and same-origin
// polling.
const contentSecurityPolicy = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// Handler serves the maintenance page from a Source.
type Handler struct {
	Source Source
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")

	switch r.URL.Path {
	case odoov1.MaintenancePagePathPrefix + "healthz":
		header.Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	case odoov1.MaintenancePagePathPrefix + "status.json":
		st := h.status(h.Source.Get())
		header.Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
		return
	}

	header.Set("Retry-After", retryAfter)
	if !isNavigation(r) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, err := h.render(r)
	if err != nil {
		log.FromContext(r.Context()).Error(err, "render the maintenance page")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Vary", "Accept-Language")
	w.WriteHeader(http.StatusServiceUnavailable)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// isNavigation tells a page load from everything else. Browsers say so in
// Sec-Fetch-Mode; without it (older clients, curl) a GET that accepts HTML is
// taken as one.
func isNavigation(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Mode") {
	case "navigate":
		return true
	case "":
		return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			strings.Contains(r.Header.Get("Accept"), "text/html")
	default:
		return false
	}
}

type pageData struct {
	Lang       string
	Dir        string
	Title      string
	Initial    string
	Accent     template.CSS
	Logo       template.URL
	Messages   map[string]string
	Status     Status
	StatusPath string
}

// status is the Snapshot's state as served, stamped with the server's clock.
func (h *Handler) status(snap Snapshot, ok bool, err error) Status {
	st := snap.Status
	if !ok {
		st = Status{State: StateUnavailable}
	}
	st.Stale = ok && err != nil
	st.Now = h.now().UTC()
	return st
}

func (h *Handler) render(r *http.Request) ([]byte, error) {
	snap, ok, err := h.Source.Get()
	page := odoov1.MaintenancePageConfig{}
	if ok {
		page = snap.Page
	}
	lang := pickLanguage(r.Header.Get("Accept-Language"))
	title := page.TitleFor(lang)
	data := pageData{
		Lang:     lang,
		Dir:      "ltr",
		Title:    title,
		Initial:  initial(title),
		Accent:   template.CSS(odoov1.DefaultMaintenancePageAccentColor),
		Messages: localized(lang, title),
		Status:   h.status(snap, ok, err),

		StatusPath: odoov1.MaintenancePagePathPrefix + "status.json",
	}
	if lang == "ar" {
		data.Dir = "rtl"
	}
	// The CRD validates both; checked again because they are written into
	// the page unescaped.
	if accent := page.AccentColorValue(); accentPattern.MatchString(accent) {
		data.Accent = template.CSS(accent)
	}
	if logoPattern.MatchString(page.Logo) {
		data.Logo = template.URL(page.Logo)
	}
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// initial is the first letter of the title, shown when there is no logo.
func initial(title string) string {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(title))
	if r == utf8.RuneError {
		return ""
	}
	return string(unicode.ToUpper(r))
}

// DefaultStatusFile is where the maintenance Deployment mounts the
// <name>-maintenance ConfigMap's status.json.
const DefaultStatusFile = odoov1.MaintenancePageStatusMountPath + "/" + odoov1.MaintenancePageStatusKey

// Run is the entry point of `odoo-operator maintenance-page`.
func Run(args []string) error {
	fs := flag.NewFlagSet(Command, flag.ContinueOnError)
	statusFile := fs.String("status-file", DefaultStatusFile, "the Snapshot the operator writes")
	listen := fs.String("listen", fmt.Sprintf(":%d", odoov1.MaintenancePagePort), "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           &Handler{Source: NewFileSource(*statusFile)},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- server.ListenAndServe() }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}
