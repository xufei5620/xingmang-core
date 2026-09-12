// rehearsal-auth-provider serves invented identities only for an isolated D
// rehearsal. Its guard binds configuration; the operator must separately prove
// the actual Docker project, owner, internal network and absence of egress.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type fixtureClient struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
	Subject    string `json:"subject"`
	Email      string `json:"email"`
}

type fixtures struct {
	SchemaVersion int                      `json:"schema_version"`
	Clients       map[string]fixtureClient `json:"clients"`
}

type fixtureGuard struct {
	SchemaVersion   int               `json:"schema_version"`
	Mode            string            `json:"mode"`
	Owner           string            `json:"owner"`
	Project         string            `json:"project"`
	BindIP          string            `json:"bind_ip"`
	NetworkCIDR     string            `json:"network_cidr"`
	ClientSourceIDs map[string]string `json:"client_source_ids"`
}

var ownerPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var projectPattern = regexp.MustCompile(`^xm-rehearsal-[a-z0-9][a-z0-9-]{0,80}$`)
var sourcePattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validateGuard(g fixtureGuard, listen, owner, project string) error {
	if g.SchemaVersion != 1 || (g.Mode != "server-rehearsal" && g.Mode != "local-synthetic") {
		return errors.New("fixture guard must explicitly select rehearsal mode")
	}
	if !ownerPattern.MatchString(g.Owner) || !projectPattern.MatchString(g.Project) || owner != g.Owner || project != g.Project {
		return errors.New("fixture owner and owned project must match the guard")
	}
	ip, ipErr := netip.ParseAddr(g.BindIP)
	network, networkErr := netip.ParsePrefix(g.NetworkCIDR)
	host, port, splitErr := net.SplitHostPort(listen)
	p, portErr := strconv.Atoi(port)
	// A public-looking address can be an isolated fixture network. This is not
	// proof of isolation: the operator validates the actual Docker network.
	if ipErr != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsLoopback() ||
		g.BindIP != ip.String() || networkErr != nil || !network.Addr().Is4() || network.Bits() < 24 ||
		network != network.Masked() || !network.Contains(ip) || splitErr != nil || host != g.BindIP || portErr != nil || p < 1 || p > 65535 {
		return errors.New("fixture listener must match a concrete IPv4 in the guarded rehearsal subnet")
	}
	if len(g.ClientSourceIDs) != 2 || !sourcePattern.MatchString(g.ClientSourceIDs["sub2api"]) ||
		!sourcePattern.MatchString(g.ClientSourceIDs["newapi"]) || g.ClientSourceIDs["sub2api"] == g.ClientSourceIDs["newapi"] {
		return errors.New("fixture guard requires distinct SUB and NEW source IDs")
	}
	return nil
}

func validateFixtures(f fixtures) error {
	if f.SchemaVersion != 1 || len(f.Clients) != 2 {
		return errors.New("fixture clients schema is invalid")
	}
	for _, platform := range []string{"sub2api", "newapi"} {
		c, ok := f.Clients[platform]
		id, err := strconv.ParseInt(c.Subject, 10, 64)
		local, domain, emailOK := strings.Cut(c.Email, "@")
		if !ok || c.Identifier == "" || len(c.Identifier) > 254 || strings.ContainsAny(c.Identifier, "\r\n\x00") ||
			len(c.Password) < 24 || len(c.Password) > 256 || !emailOK || local == "" || !strings.HasSuffix(domain, ".invalid") ||
			strings.ContainsAny(c.Email, "\r\n\t ") || strings.Count(c.Email, "@") != 1 || err != nil || id <= 0 || strconv.FormatInt(id, 10) != c.Subject {
			return errors.New("fixture identities must be invented accounts with distinct generated credentials")
		}
		if platform == "sub2api" && c.Identifier != c.Email {
			return errors.New("SUB fixture identifier must equal its invented email")
		}
	}
	if f.Clients["sub2api"].Password == f.Clients["newapi"].Password || f.Clients["sub2api"].Email == f.Clients["newapi"].Email {
		return errors.New("fixture clients must be distinct")
	}
	return nil
}

func readJSONFile(path string, private bool, out any) error {
	if err := requireRegularFile(path, private); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("fixture input could not be opened")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	if err := decoder.Decode(out); err != nil {
		return errors.New("fixture input JSON is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("fixture input must contain one JSON document")
	}
	return nil
}

func requireRegularFile(path string, private bool) error {
	if !filepath.IsAbs(path) {
		return errors.New("fixture input path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return errors.New("fixture input must be a bounded regular file")
	}
	// Windows ACL ownership is checked by the outer operator; POSIX permission
	// bits are meaningful inside the server's private tmpfs mount.
	if private && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("fixture private input permissions are too broad")
	}
	return nil
}

func loadConfiguration(private, guard, listen, owner, project string) (fixtures, error) {
	var g fixtureGuard
	if err := readJSONFile(guard, false, &g); err != nil {
		return fixtures{}, err
	}
	if err := validateGuard(g, listen, owner, project); err != nil {
		return fixtures{}, err
	}
	var f fixtures
	if err := readJSONFile(private, true, &f); err != nil {
		return fixtures{}, err
	}
	return f, validateFixtures(f)
}

type session struct {
	platform string
	expires  time.Time
}
type provider struct {
	fixtures fixtures
	owner    string
	mu       sync.Mutex
	sessions map[string]session
}

func newProvider(f fixtures) *provider {
	return &provider{fixtures: f, sessions: make(map[string]session)}
}

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !ownerPattern.MatchString(p.owner) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, 200, map[string]any{"fixture": true, "owner": p.owner})
	case "/api/v1/auth/login":
		p.login(w, r, "sub2api", "email")
	case "/api/user/login":
		p.login(w, r, "newapi", "username")
	case "/api/v1/auth/me":
		p.profile(w, r, "sub2api")
	case "/api/user/self":
		p.profile(w, r, "newapi")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *provider) login(w http.ResponseWriter, r *http.Request, platform, field string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body map[string]string
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := decoder.Decode(&body); err != nil || len(body) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c := p.fixtures.Clients[platform]
	if subtle.ConstantTimeCompare([]byte(body[field]), []byte(c.Identifier)) != 1 || subtle.ConstantTimeCompare([]byte(body["password"]), []byte(c.Password)) != 1 {
		if platform == "sub2api" {
			writeJSON(w, 401, map[string]any{"code": 401, "message": "invalid credentials"})
		} else {
			writeJSON(w, 200, map[string]any{"success": false, "message": "invalid credentials"})
		}
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		w.WriteHeader(503)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	p.mu.Lock()
	for key, value := range p.sessions {
		if time.Now().After(value.expires) {
			delete(p.sessions, key)
		}
	}
	if len(p.sessions) >= 64 {
		p.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	p.sessions[token] = session{platform: platform, expires: time.Now().Add(15 * time.Minute)}
	p.mu.Unlock()
	data := map[string]any{"user": userData(c)}
	if platform == "sub2api" {
		data["access_token"] = token
		writeJSON(w, 200, map[string]any{"code": 0, "data": data})
	} else {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/api/user/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 900})
		writeJSON(w, 200, map[string]any{"success": true, "data": data})
	}
}

func (p *provider) profile(w http.ResponseWriter, r *http.Request, platform string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := ""
	if platform == "sub2api" {
		token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	} else if c, err := r.Cookie("session"); err == nil {
		token = c.Value
	}
	p.mu.Lock()
	s, ok := p.sessions[token]
	p.mu.Unlock()
	if !ok || s.platform != platform || !s.expires.After(time.Now()) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if platform == "sub2api" {
		writeJSON(w, 200, map[string]any{"code": 0, "data": userData(p.fixtures.Clients[platform])})
	} else {
		writeJSON(w, 200, map[string]any{"success": true, "data": userData(p.fixtures.Clients[platform])})
	}
}

func userData(c fixtureClient) map[string]any {
	id, _ := strconv.ParseInt(c.Subject, 10, 64)
	return map[string]any{"id": id, "username": c.Identifier, "display_name": c.Identifier, "email": c.Email}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func run(args []string) error {
	flags := flag.NewFlagSet("invoice-rehearsal-auth-provider", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	private := flags.String("fixtures", "", "absolute private fixture JSON")
	guard := flags.String("fixture-guard", "", "absolute owned rehearsal guard JSON")
	listen := flags.String("listen", "", "explicit guarded IPv4:port")
	owner := flags.String("owner", "", "32 hexadecimal rehearsal owner")
	project := flags.String("project", "", "owned rehearsal project")
	cert := flags.String("tls-cert", "", "absolute fixture TLS certificate")
	key := flags.String("tls-key", "", "absolute private fixture TLS key")
	generate := flags.Bool("generate-tls", false, "create a fresh fixture CA and TLS key")
	shred := flags.String("shred-fixtures", "", "overwrite and unlink only the guarded owned fixture mount contents")
	directory := flags.String("tls-directory", "", "absolute private TLS output directory")
	hosts := flags.String("tls-hosts", "", "comma-separated DNS certificate names")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("fixture provider arguments are invalid")
	}
	if *generate && *shred != "" {
		return errors.New("fixture provider maintenance modes are mutually exclusive")
	}
	if *shred != "" {
		var g fixtureGuard
		if err := readJSONFile(*guard, false, &g); err != nil {
			return err
		}
		if err := validateGuard(g, *listen, *owner, *project); err != nil {
			return err
		}
		return shredFixtures(*shred, g, *guard)
	}
	if *generate {
		var g fixtureGuard
		if err := readJSONFile(*guard, false, &g); err != nil {
			return err
		}
		if err := validateGuard(g, *listen, *owner, *project); err != nil {
			return err
		}
		return generateTLS(*directory, strings.Split(*hosts, ","))
	}
	f, err := loadConfiguration(*private, *guard, *listen, *owner, *project)
	if err != nil {
		return err
	}
	if requireRegularFile(*cert, false) != nil || requireRegularFile(*key, true) != nil {
		return errors.New("fixture TLS requires absolute certificate and private key files")
	}
	pair, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		return errors.New("fixture TLS certificate could not be loaded")
	}
	handler := newProvider(f)
	handler.owner = *owner
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, ErrorLog: log.New(io.Discard, "", 0)}
	if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("fixture TLS listener stopped")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
