package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/admin"
	"github.com/0xBrsm/NexQuake/nexus/internal/assets"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
	"github.com/0xBrsm/NexQuake/nexus/trunk"
)

// headerNexQuakeRef is the response header carrying the manifest session ref
// returned by /start. The WASM client echoes it back when fetching assets so
// expired sessions can be detected.
const headerNexQuakeRef = "X-NexQuake-Ref"

// Register MIME types for static-asset extensions http.FileServer's default
// table doesn't cover (or that older Go versions sniff incorrectly). Registering
// here makes mime.TypeByExtension authoritative for the file server, dropping
// the need for a per-request Content-Type middleware.
func init() {
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".data", "application/octet-stream")
	_ = mime.AddExtensionType(".pak", "application/octet-stream")
}

// rconLoginLandingTmpl is served at GET /rcon. It backs two popup login flows,
// distinguished by whether the URL carries an OAuth `code`/`error`:
//
//   - Edge-gated (no query): a fronting access gate (e.g. Cloudflare Access on
//     the /rcon path) ran its IdP flow and landed the popup back here with its
//     cookie set. The page relays the authorization outcome Nexus computed for
//     this hit (the %t) to the opener through localStorage, then closes.
//   - Client-side PKCE (code present): Nexus is exposed directly with OIDC
//     configured, so the shell itself drove the Authorization Code flow and the
//     IdP redirected the popup back here. The page relays the code+state to the
//     opener through localStorage, then closes.
//
// Both hand off through storage rather than postMessage because the WASM page
// sets COOP-same-origin (for SharedArrayBuffer), which severs window.opener the
// moment the popup navigates to the IdP. localStorage is shared per-origin and
// fires a `storage` event in the opener, so it survives that severance. The key
// string must match the client shell's LOGIN_CALLBACK_KEY (55-rcon.js).
const rconLoginLandingTmpl = `<!doctype html>
<html><head><meta charset="utf-8"><title>rcon</title></head>
<body><script>
(function(){
  try {
    var p = new URLSearchParams(location.search);
    var code = p.get('code'), state = p.get('state'), error = p.get('error');
    if (code || error) {
      localStorage.setItem('nq_rcon_oidc_cb', JSON.stringify({
        code: code || '', state: state || '', error: error || ''
      }));
    } else {
      localStorage.setItem('nq_rcon_oidc_cb', JSON.stringify({ authorized: %t }));
    }
  } catch (e) {}
  window.close();
})();
</script>
<p>Authenticated. You can close this window.</p>
</body></html>
`

// renderRconLanding fills rconLoginLandingTmpl with the edge-gated authorization
// outcome. The PKCE branch ignores it (that hit carries no admin creds yet — the
// token exchange happens later at POST /rcon/session).
func renderRconLanding(authorized bool) string {
	return fmt.Sprintf(rconLoginLandingTmpl, authorized)
}

// rconMaxBody caps the size of a POST /rcon body. Admin JSON-RPC envelopes
// are tiny — method string + a handful of params — so 8 KiB is generous.
const rconMaxBody = 8 << 10

// startManifestBundle is the JSON envelope served at /start. The asset server
// owns the game/cd entries; client config and any transport-routing fields
// are assembled here from runtimeConfig and registered transport providers.
type startManifestBundle struct {
	Client map[string]any                         `json:"client"`
	Game   map[string][]assets.StartManifestEntry `json:"game"`
	CD     []assets.StartManifestEntry            `json:"cd,omitempty"`
}

// AddBootstrapClientFields registers a callback that contributes fields to the
// "client" object in /start responses. Called per request so transport-specific
// live values (and request-derived ones like the authority in r.Host)
// propagate within one fetch. Transports use this to add their routing info
// without the asset server knowing about HTTP layering.
func (app *nexusApp) AddBootstrapClientFields(fn func(r *http.Request) map[string]any) {
	app.bootstrapClientFields = append(app.bootstrapClientFields, fn)
}

// handleStart issues a fresh per-session asset manifest, merges static and
// transport-contributed client config, and returns the base64-encoded JSON
// envelope. The X-NexQuake-Ref header carries the session ref clients echo
// back when fetching assets.
func (app *nexusApp) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	game, cd, ref, err := app.assetServer.IssueManifest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(game) == 0 {
		http.NotFound(w, r)
		return
	}

	client := map[string]any{
		"prefetchConcurrency": app.cfg.vfsPrefetchConcurrency,
		"smenuOnFirstLoad":    app.cfg.clientAutoSMenu,
		"urlArgs":             app.cfg.clientURLArgs,
	}
	if len(app.cfg.clientSendArgs) > 0 {
		client["sendArgs"] = app.cfg.clientSendArgs
	}
	for _, p := range app.bootstrapClientFields {
		for k, v := range p(r) {
			client[k] = v
		}
	}

	bundle, err := json.Marshal(startManifestBundle{Client: client, Game: game, CD: cd})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set(headerNexQuakeRef, ref)
	_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(bundle))
}

// handleServersTxt serves the FTE client's HTTP master list ("masterhttp"): one
// line per server, "<connect-url> <infostring>". Every entry points at this
// origin's trunk /connect so the in-game browser only ever lists Nexus-managed
// games (never the public FTE master), and joining tunnels through Nexus. The
// server's live status is fetched with a one-shot getinfo probe.
func (app *nexusApp) handleServersTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	// Advertise the secure scheme when the page is served over TLS so the FTE
	// client maps it to wss:// (a plain ws:// handshake against the TLS port is
	// rejected). r.TLS is set when Nexus terminates TLS itself; the forwarded
	// header covers a TLS-terminating proxy in front.
	scheme := "trunk"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "trunks"
	}
	connectURL := scheme + "://" + r.Host + "/connect"
	info, ok := orch.ProbeServerInfostring(app.cfg.gameServerAddr, time.Second)
	if !ok {
		// Keep the server listed (and joinable) even if the status probe blips.
		info = `\hostname\NexQuake-FTE\mapname\?\clients\0\maxclients\0\`
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", connectURL, info)
}

// trunkSession runs the per-request bookkeeping that's identical across
// tunnel transports: identity resolution, connect/disconnect logging, and
// bridging the transport-specific upgrade into trunk.NewConn via the supplied
// closure. trunk's Registry tracks the connection's session metadata for /rcon
// session views; admin promotion is purely a per-/rcon-request concept and is
// not stored on the session.
func (app *nexusApp) trunkSession(
	w http.ResponseWriter, r *http.Request,
	transportName string,
	upgrade func() (trunk.Transport, error),
) {
	req := app.access.Request(r)
	client := req.Client

	transport, err := upgrade()
	if err != nil {
		// Client-driven, per-connection noise: scanners and stray probes hit
		// the upgrade endpoint constantly and fail it. Debug, like the TLS
		// handshake noise from the same scanners (see newServerErrorLog).
		slog.Debug(fmt.Sprintf("%s upgrade failed: %v", transportName, err))
		return
	}

	displayAddr := client.SourceIP
	if displayAddr == "" {
		displayAddr = r.RemoteAddr
	} else if _, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		displayAddr = net.JoinHostPort(displayAddr, port)
	}

	tc, err := app.trunk.NewSession(transport, client.SourceIP)
	if err != nil {
		slog.Error(fmt.Sprintf("Failed to create trunk session: %v", err))
		_ = transport.Close()
		return
	}

	// The trunk tunnel is a protocol-agnostic datagram pipe: the FTE client
	// runs its own native handshake over it and never needs to be told its
	// VirtualIP, so no identity frame is announced. Nexus still binds the
	// per-session UDP socket to the allocated VirtualIP server-side, which is
	// what keeps fteqw-sv's view of clients distinct.
	vip := tc.VirtualIP()
	vipStr := net.IP(vip[:]).String()
	slog.Info(fmt.Sprintf("%s connected (%s)", client.ID, transportName),
		"addr", displayAddr, "vip", vipStr)

	detachClient := app.clients.Attach(tc, client)
	defer detachClient()

	tc.Run()

	slog.Info(fmt.Sprintf("%s disconnected (%s)", client.ID, transportName),
		"addr", displayAddr, "vip", vipStr)
}

// handleControlFrame wires the trunk's port-0 control channel: slist
// requests only. Other port-0 payloads are silently dropped; admin rcon
// is served separately by POST /rcon.
func (app *nexusApp) handleControlFrame(s *trunk.Session, payload []byte) {
	if orch.IsSlistRequest(payload) {
		// Non-blocking: this runs on the session's read loop, and a full tx
		// queue means a stalled client — dropping the reply mirrors UDP, and
		// the engine re-sends slist requests anyway.
		_ = s.TrySendControl(app.serverMgr.BuildSlistResponse())
	}
}

// logUnauthorizedClaimKeys logs (at debug) the claim keys a verified-but-not-
// admin token carried, so an operator can target AUTH_ADMIN_ID against what the
// IdP actually emits (keys vary: `groups` vs Entra GUIDs vs Auth0 namespaced,
// and some IdPs omit `email`). No-op for an empty claim set.
func logUnauthorizedClaimKeys(reason string, claims map[string]any) {
	if len(claims) == 0 {
		return
	}
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	slog.Debug(fmt.Sprintf("rcon: %s; claims present: %v", reason, keys))
}

// rconSessionMaxBody caps the POST /rcon/session body — a small JSON of the
// authorization code plus PKCE verifier.
const rconSessionMaxBody = 8 << 10

// handleRconSession is the server-side half of the browser's client-side PKCE
// login (hybrid BFF). The browser drives the authorize redirect + popup, then
// POSTs the resulting code and PKCE verifier here; Nexus exchanges them at the
// IdP token endpoint server-to-server (sidestepping the IdP's missing CORS),
// verifies the id_token, and hands it back as an httpOnly nq_session cookie so
// the token never enters page JavaScript. The login outcome is echoed to the
// player's console over the trunk control channel.
func (app *nexusApp) handleRconSession(w http.ResponseWriter, r *http.Request) {
	if !app.access.PKCEEnabled() {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rconSessionMaxBody))
	if err != nil {
		http.Error(w, "request body too large", http.StatusBadRequest)
		return
	}
	var in struct {
		Code     string `json:"code"`
		Verifier string `json:"code_verifier"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.Code == "" || in.Verifier == "" {
		http.Error(w, "code and code_verifier are required", http.StatusBadRequest)
		return
	}

	// Derive redirect_uri from the request authority so it matches what the
	// browser sent to the authorize endpoint (origin + /rcon) without trusting a
	// client-supplied value. EXTERNAL_URL being set means we're served over TLS.
	redirectURI := "https://" + r.Host + "/rcon"
	idToken, claims, expiry, err := app.access.ExchangeCode(r.Context(), in.Code, in.Verifier, redirectURI)
	if err != nil {
		slog.Debug(fmt.Sprintf("rcon: PKCE token exchange failed: %v", err))
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}
	if len(idToken) > access.SessionCookieMaxLen {
		http.Error(w, "id_token too large for cookie session", http.StatusRequestEntityTooLarge)
		return
	}

	maxAge := 0
	if !expiry.IsZero() {
		if secs := int(time.Until(expiry).Seconds()); secs > 0 {
			maxAge = secs
		}
	}
	// Set the cookie even if the account isn't an admin: the token is valid, and
	// admin authorization is re-evaluated per request at POST /rcon. The client
	// uses the {authorized} reply only to choose which toast to show.
	http.SetCookie(w, &http.Cookie{
		Name:     access.SessionCookieName,
		Value:    idToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	authorized := app.access.AuthorizeClaims(claims)
	if !authorized {
		logUnauthorizedClaimKeys("PKCE login verified but not an admin", claims)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"authorized": authorized})
}

// handleRcon is the POST /rcon JSON-RPC handler. It reads the envelope,
// authorizes the request via the Authorization header (Bearer for OIDC JWT,
// Rcon for the shared-secret password) and dispatches to the admin command
// registry. Authorization is per-request — there is no persistent admin flag
// on the connection.
func (app *nexusApp) handleRcon(w http.ResponseWriter, r *http.Request) {
	reqCtx := app.access.Request(r)
	client := reqCtx.Client

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, rconMaxBody))
	if err != nil {
		writeRPC(w, nil, admin.ErrCodeInvalidReq, "request body too large or unreadable")
		return
	}

	req, errResp := admin.ParseRequest(body)
	if errResp != nil {
		writeResponse(w, errResp)
		return
	}

	if !app.access.AuthorizeAdmin(reqCtx, r) {
		app.admin.AuditUnauthorized(client, req.Method)
		// A verified JWT that still fails authorization means the admin rules
		// matched no claim. Surface the claim keys the token carried so an
		// operator can see exactly what AUTH_ADMIN_ID has to work with (e.g.
		// whether the IdP emitted `groups` at all, and under what key).
		logUnauthorizedClaimKeys("login verified but no admin rule matched", reqCtx.Claims)
		writeResponse(w, &admin.Response{
			Jsonrpc: "2.0",
			Error: &admin.RPCError{
				Code:    admin.ErrCodeUnauthorized,
				Message: "unauthorized",
				Data:    map[string]any{"hint": app.access.UnauthorizedHint()},
			},
			ID: req.ID,
		})
		return
	}

	resp := app.admin.Dispatch(req, client)
	writeResponse(w, resp)
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeResponse(w, &admin.Response{
		Jsonrpc: "2.0",
		Error:   &admin.RPCError{Code: code, Message: message},
		ID:      id,
	})
}

func writeResponse(w http.ResponseWriter, resp *admin.Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// newMux builds the transport-agnostic HTTP router. Transport setup functions
// mount their implementation on the shared /connect route. The caller wraps with
// access.HTTPGate at server-construction time so blocklisted sources are
// rejected before any route-specific handler runs.
//
// Routes mounted here:
//   - GET /health           — liveness probe, returns version headers
//   - GET /rcon             — auto-closing landing page for popup login flows
//   - POST /rcon            — admin JSON-RPC endpoint
//   - POST /rcon/session    — server-side PKCE token exchange → httpOnly cookie
//   - /start                — client bootstrap manifest
//   - /nq/                  — hashed game assets (pak files, etc.)
//   - /                     — WASM client static files
func (app *nexusApp) newMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		v := currentVersionInfo()
		w.Header().Set("X-NexQuake-Nexus-GitSHA", v.GitSHA)
		w.Header().Set("X-NexQuake-Nexus-BuildTime", v.BuildTime)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// GET /rcon is the in-game rcon shell's login popup target, for both the
	// edge-gated flow (a fronting access gate round-trips its IdP and lands
	// back here with its cookie set) and the client-side PKCE callback (the
	// IdP redirects back here with a `code`). See rconLoginLandingTmpl for how
	// the page distinguishes them. For the edge flow the landing page relays the
	// authorization outcome — computed here via AuthorizeAdmin — to the opener
	// through localStorage; the shell turns both flows' outcomes into the login
	// result (a console line, plus a toast on touch devices).
	mux.HandleFunc("GET /rcon", func(w http.ResponseWriter, r *http.Request) {
		authorized := app.access.AuthorizeAdmin(app.access.Request(r), r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, renderRconLanding(authorized))
	})

	mux.HandleFunc("POST /rcon", app.handleRcon)

	// POST /rcon/session: server-side token exchange for the client-side PKCE
	// login. 404s when PKCE isn't offered (see handleRconSession).
	mux.HandleFunc("POST /rcon/session", app.handleRconSession)

	mux.Handle("/start", addIsolationHeaders(http.HandlerFunc(app.handleStart)))
	mux.Handle("/nq/", addIsolationHeaders(app.assetServer.AssetHandler()))

	// FTE in-game server browser (masterhttp): lists only Nexus-managed games.
	mux.HandleFunc("GET /servers.txt", app.handleServersTxt)

	clientFS := http.FileServerFS(os.DirFS(app.cfg.clientDir))
	mux.Handle("/", addIsolationHeaders(cacheControlClient(clientFS)))

	return mux
}

// cacheControlClient sets Cache-Control headers for the WASM client static
// files. HTML, JS, WASM, and CSS are served with no-store to prevent stale
// client/server version mismatches. Skips setting headers if a reverse proxy
// has already set Cache-Control.
func cacheControlClient(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get("Cache-Control") == "" {
			path := r.URL.Path
			isHTML := path == "/" || strings.HasSuffix(path, ".html")
			isAsset := strings.HasSuffix(path, ".js") ||
				strings.HasSuffix(path, ".wasm") ||
				strings.HasSuffix(path, ".css")
			if isHTML || isAsset {
				w.Header().Set("Cache-Control", "no-store")
			}
			if isAsset {
				// no-store (not no-cache) to avoid intermittent JS/WASM
				// mismatches observed across browsers and proxies.
				w.Header().Set("Pragma", "no-cache")
				w.Header().Set("Expires", "0")
			}
		}
		h.ServeHTTP(w, r)
	})
}

// addIsolationHeaders sets the COOP/COEP/CORP headers required for
// SharedArrayBuffer (used by WASM threading) on all responses from h.
func addIsolationHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}
