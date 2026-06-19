# FTE Trunk Transport — Implementation Design

**Status:** design only (not yet implemented)
**Goal:** Let the FTE WebAssembly/Emscripten browser client reach a plain‑UDP `fteqw-sv`
through the Nexus relay, instead of via FTE's native WebSocket/WebRTC paths.

```
Browser FTE (WASM)
  --(WS, later WebTransport)-->  Nexus /connect
  --(2‑byte BE port‑prefix demux to localhost UDP)-->  fteqw-sv (plain UDP Quake server)
```

Nexus's "trunk" tunnel multiplexes datagrams over a single WS/WT connection. Every frame is
`[portHi][portLo][payload]`. Port 0 is the control channel. On connect Nexus sends a control
frame `"NQIP"` + 4‑byte VirtualIP on port 0. Nexus binds a per‑session UDP socket to that
deterministic `127.x.x.x` VirtualIP so `fteqw-sv` sees distinct sources. From FTE's view the
trunk is a single datagram pipe to "the server"; outbound packets carry the server's UDP port in
the prefix, inbound frames carry the source port.

All file/line references below are to the extracted engine at
`.ftesrc/engine/...`, which corresponds to `engine/...` inside the upstream FTE git tree (the
prefix the repo's existing `patches/fteqw-menu.patch` uses, e.g. `a/engine/client/cl_main.c`).

---

## 0. Key facts established from the source

These shaped every decision below; they are not assumptions.

1. **The browser build has exactly one datagram transport today, and it is already a thin
   JS shim.** `FTE_TARGET_WEB` defines `HAVE_WEBSOCKCL` (`common/net.h:24‑26`) and undefines
   `HAVE_IPV4`/`HAVE_IPV6`/`HAVE_IPX` (`common/netinc.h:239‑243`). So in the browser there are no
   real sockets — `NA_IP` does not even exist. The only client datagram path is
   `FTENET_WebSocket_*` driving `emscriptenfte_ws_connect/_send/_recv/_close`
   (`common/net_wins.c:8798‑8827`, `9269‑9315`; JS in `web/ftejslib.js:1209‑1300`).

2. **The web build is poll‑driven, not Asyncify.** FTE's emscripten link flags
   (`Makefile:1888‑1903`) contain **no** `ASYNCIFY`. The main loop is a cooperative
   `setTimeout`/`requestAnimationFrame` reschedule (`web/ftejslib.js` `FTEC.step`, ~131‑153),
   `Module.noExitRuntime=true`, frame fn registered via `emscriptenfte_setupmainloop(Sys_MainLoop)`
   (`web/sys_web.c:458`). `Sys_Sleep` is a no‑op (`web/sys_web.c:599‑602`). Networking is **never**
   pushed into C; `WebSocket.onmessage` only appends to a JS queue (`s.inq`), and C drains it each
   frame inside `Host_Frame` → `NET_ReadPackets` → `con->GetPacket()`. **This is the opposite of
   the NexQuake reference**, which is built around `-sASYNCIFY=1` and an `emscripten_sleep` wait in
   `WASM_SendPacket`. We must follow FTE's poll model, not NexQuake's blocking model.

3. **FTE's connection identity is the QuakeWorld `getchallenge` handshake, not the source IP.**
   `CL_SendConnectPacket`/`CL_CheckForResend` send connectionless `\xff\xff\xff\xffgetchallenge …`
   (`client/cl_main.c:1500‑1557`, `4151`), and the default connect mode `CIM_DEFAULT` sends both a
   QW getchallenge and an NQ connect (`client/cl_main.c:317`). The server keys the session on the
   challenge it minted, **not** on the client's source address. NetQuake's landriver needed the
   VirtualIP because NQ keys clients by source `qsockaddr`; FTE does not depend on it the same way.
   **Conclusion (see §2): FTE does not need NQIP to function — Nexus still uses the VirtualIP
   server‑side to keep UDP sources distinct, but FTE can simply consume‑and‑ignore the NQIP frame.**
   We still store it for diagnostics and correct `NET_AdrToString` output.

4. **`emscriptenfte_ws_recv` truncates, one datagram per call.** It `shift()`s one queued
   `Uint8Array`, truncates to the C buffer (`web/ftejslib.js:1289‑1295`), returns `>0`/`0`/`-1`.
   `FTENET_WebSocket_GetPacket` calls it once per invocation and returns `true` while `>0`
   (`common/net_wins.c:8798‑8811`); `NET_ReadPackets` loops `GetPacket` until false
   (`common/net_wins.c:8801`, `9465`). Our trunk recv must obey the same one‑frame‑per‑call,
   drain‑until‑0 contract.

5. **The send/recv frame body must be pure Quake protocol.** `net_message_buffer` is what the
   netchan layer parses (`common/net.h:122`); `FTENET_WebSocket_GetPacket` writes straight into it
   (`common/net_wins.c:8801`). The 2‑byte port prefix is **trunk wire framing only** and must be
   added on send / stripped on recv *below* the point where FTE sees the bytes.

---

## 1. Insertion point

### The two options, with evidence

**Option (a): new `NA_TRUNK` + `NP_TRUNK` + new URL scheme (`trunk://`).**
Would require touching the `netadrtype_t`/`netproto_t` enums (`common/net.h:34‑69`), the address
union (`common/net.h:80‑104`), `NET_AdrToString`/`NET_AdrToStringDP` (two copies of the prot/type
switches at `net_wins.c:831‑863` and `~1080‑1130`), `NetadrToSockadr`/`SockadrToNetadr`
(`net_wins.c:321‑325`, `396‑398`), `NET_CompareAdr`/`NET_CompareBaseAdr`
(`net_wins.c:535‑537`, `699‑701`), the URI scheme table (`net_wins.c:1600‑1657`), `NET_StringToAdr2`
(`net_wins.c:1715‑1824`), and a new arm in the establish dispatch (`net_wins.c:3525‑3530`). That is
~10 switch/table sites, each compiled only because a brand‑new enumerator was added — high churn,
and every `switch(a->type)` without a `default` becomes a `-Werror` risk.

**Option (b): reuse `NA_WEBSOCKET` + a new `NP_*` protocol value, intercepting at establish time.**
`NA_WEBSOCKET` already carries a `char websocketurl[64]` (`common/net.h:94‑96`) — exactly the field
we need to hold the `/connect` URL. It already round‑trips through `NetadrToSockadr`
(`net_wins.c:321‑325`), `SockadrToNetadr` (`net_wins.c:396‑398`), `NET_CompareAdr`/`BaseAdr`
(`net_wins.c:535‑537`, `699‑701`), and both `AdrToString` copies (`net_wins.c:853‑862`,
`1106‑1115`) — **all of which already special‑case `NA_WEBSOCKET` and print the URL**. So reusing
the type means zero edits to address marshalling/printing/compare. We only need:
  - one new `netproto_t` enumerator, `NP_TRUNK`, inserted in `common/net.h:54‑69`;
  - URL‑scheme table rows mapping `trunk://`/`trunks://` to `{NP_TRUNK, NA_WEBSOCKET}`
    (`net_wins.c:1630‑1635`, inside the `HAVE_WEBSOCKCL` arm);
  - StringToAdr recognition of `trunk://`/`trunks://` (and optionally a bare host when a
    `cl_trunk` cvar is set) writing `type=NA_WEBSOCKET, prot=NP_TRUNK, websocketurl=<url>`
    (`net_wins.c:1767‑1776` is the model to copy);
  - two new dispatch lines in `FTENET_ClientPort`/establish (`net_wins.c:3525‑3530`):
    `if (adr->prot == NP_TRUNK && adr->type == NA_WEBSOCKET) establish = FTENET_Trunk_EstablishConnection;`
  - `NP_TRUNK` arms in the two `AdrToString` prot switches and the `NET_AddrIsTLS`‑style checks
    (`net_wins.c:843‑847`, `1096‑1097`, `2286`, `8021`) — trivial `case NP_TRUNK: prot="trunk://";`.

### Recommendation: **Option (b)** — reuse `NA_WEBSOCKET`, add `NP_TRUNK`.

`NA_WEBSOCKET` is *already* "an opaque URL‑addressed datagram pipe whose family the net layer never
introspects beyond comparing the URL string." That is precisely what trunk is. A new `NP_TRUNK`
distinguishes trunk from `NP_WS`/`NP_WSS` at exactly the one place that matters — the establish
dispatch — without disturbing the address plumbing. This is also how FTE already layers RTC over
`NA_WEBSOCKET` (`NP_RTC_TCP`/`NP_RTC_TLS` reuse the same type, `net_wins.c:1718‑1722`, `3528‑3529`),
so we are following an established in‑tree pattern rather than inventing one.

**Exact functions to patch (option b):**

| Site | File:line | Change |
|---|---|---|
| `netproto_t` enum | `common/net.h:54‑69` | add `NP_TRUNK` (and optional `NP_TRUNKS`) before `NP_INVALID` |
| URI scheme table | `common/net_wins.c:1630‑1635` | add `{"trunk://", NP_TRUNK, NA_WEBSOCKET, URISCHEME_NEEDSRESOURCE}` (+ `trunks://`) in the `HAVE_WEBSOCKCL` branch |
| `NET_StringToAdr2` | `common/net_wins.c:1767‑1776` | recognise `trunk(s)://`, copy URL into `websocketurl`, set `prot=NP_TRUNK` |
| establish dispatch | `common/net_wins.c:3525‑3530` | route `NP_TRUNK`+`NA_WEBSOCKET` → `FTENET_Trunk_EstablishConnection` |
| `NET_AdrToString` | `common/net_wins.c:843‑847` | `case NP_TRUNK: prot="trunk://"; break;` |
| `NET_AdrToStringDP` | `common/net_wins.c:1096‑1097` | same |
| TLS predicate(s) | `common/net_wins.c:2286`, `8021` | treat `NP_TRUNKS` like `NP_WSS` if we add a secure variant |
| new code | `common/net_wins.c` (new block under `#ifdef FTE_TARGET_WEB`, near `8761‑9368`) | `FTENET_Trunk_*` connection impl (see §3/§4) |

---

## 2. Address model

### "The server"
A connected trunk is represented by a single `ftenet_trunk_connection_t` whose `remoteadr` is
`{ type=NA_WEBSOCKET, prot=NP_TRUNK, websocketurl="trunk://host/connect", port=0 }`. This mirrors
`FTENET_WebSocket_EstablishConnection`, which stores `adr` then zeroes `adr.port`
(`common/net_wins.c:9309‑9310`). FTE matches inbound packets to this connection by
`NET_CompareAdr(to,&wsc->remoteadr)` in SendPacket (`common/net_wins.c:8817`) and stamps
`net_from=remoteadr` on recv (`common/net_wins.c:8804`). For trunk we keep the *same* single
`remoteadr` for FTE‑facing purposes — FTE only ever talks to "the server" as one peer.

### The 2‑byte port prefix ↔ `netadr_t`
This is the crux: **FTE must not see the port prefix, and FTE's `netadr_t.port` is not the trunk
destination port.** The trunk dest/source port is a *Nexus routing detail* that lives entirely
inside `FTENET_Trunk_*`:

- **Outbound:** FTE hands `SendPacket(con, len, data, to)` a pure Quake datagram. The trunk layer
  prepends `[server_port_hi][server_port_lo]` and sends `len+2` bytes. The server port is the UDP
  port `fteqw-sv` listens on, known to the trunk layer as a fixed value (default 26000) — see "port
  source" below.
- **Inbound:** trunk reads the first 2 bytes as the source port, strips them, copies the remaining
  `len-2` bytes into `net_message_buffer`, sets `net_from=remoteadr`, returns true. FTE sees a
  normal datagram from "the server."
- **Port 0 / control channel:** never surfaced to FTE. Handled inside the trunk recv demux (NQIP,
  and any future relay control messages). Quake game/connectionless traffic always rides a
  non‑zero port.

**Where does the server port come from?** Two viable sources, decide at implementation:
1. **Fixed default (recommended for v1):** trunk always sends to port 26000 (`PORT_DEFAULTSERVER`),
   matching a single `fteqw-sv -port 26000` behind Nexus. Simplest; Nexus already demuxes by its own
   session table, and a browser only ever connects to "the" server behind that `/connect`.
2. **Learned from the URL fragment:** `NET_AdrToString` already supports a `#port` suffix on
   `NA_WEBSOCKET` (`common/net_wins.c:857‑858`), and `StringToAdr` could parse `trunk://host/connect#26000`
   into `adr.port`. Then the trunk layer uses `ntohs(remoteadr.port)` as the dest port. Keep this as
   the override path; default to 26000 when absent.

### NQIP / VirtualIP in FTE
Per §0.3, FTE keys its session on the `getchallenge` handshake, so the VirtualIP is **not required
for FTE to connect or stay connected** — Nexus uses it server‑side to bind distinct UDP sources, and
that is invisible to FTE. We therefore:
- Detect the NQIP control frame on port 0 (8 bytes, first 4 == `"NQIP"`) inside the trunk recv demux,
  copy the 4‑byte VirtualIP into the connection struct (`byte virtualip[4]; qboolean virtualip_set;`),
  and **consume it** (do not surface to FTE).
- Optionally fold it into `remoteadr`/local‑address reporting only for cosmetic correctness
  (`NET_LocalAddressForRemote` via a `GetLocalAddresses` hook, `common/net_wins.c:9506‑9519`) — not
  load‑bearing.

This is a deliberate simplification of the NexQuake `net_nqchan.c` model, which had to stamp the
VirtualIP into the client `qsockaddr` (`FillClientAddr`) because NetQuake's landriver compares source
addresses. FTE has no equivalent dependency.

---

## 3. Browser JS glue

Follow FTE's existing pattern exactly: a `mergeInto(LibraryManager.library, { … })` entry in
`web/ftejslib.js`, declared `extern` in `web/ftejslib.h`, linked via `--js-library web/ftejslib.js`
(`Makefile:1888`). **Do not** use NexQuake's `EM_JS`/`-lwebsocket.js` style — FTE doesn't enable it
and `ERROR_ON_UNDEFINED_SYMBOLS=1` (`Makefile:1898`) will reject anything not wired through the
js‑library.

The existing WS socket object shape is `{ws, inq, err, con}` with `inq` an array of `Uint8Array`
(`web/ftejslib.js:1214`), allocated through the `FTEH.h[]` handle table
(`web/ftejslib.js:40‑53`, `prejs.js:2‑3`). We reuse this verbatim — trunk over WS *is* a WebSocket;
the only differences vs `emscriptenfte_ws_*` are (a) the URL ends in `/connect` and (b) we may want a
distinct subprotocol string. In fact **for the WS variant we can reuse `emscriptenfte_ws_connect`
unchanged** and only add framing in C. New JS functions are needed only when WebTransport lands.

To keep one clean abstraction and leave room for WT, add a thin trunk‑specific set that internally
delegates to the WS implementation for now:

```js
// web/ftejslib.js — add inside mergeInto(LibraryManager.library, { ... })

emscriptenfte_trunk_open__deps: ['emscriptenfte_handle_alloc'],
emscriptenfte_trunk_open : function(curl)
{   // curl: C string, full "ws(s)://host/connect" already resolved by C
    var _url = UTF8ToString(curl);
    var s = {ws:null, inq:[], err:0, con:0, kind:'ws'};
    try { s.ws = new WebSocket(_url, 'fteqw-trunk'); } catch(e){ console.log(e); }
    if (s.ws == null || s.ws === undefined) return -1;
    s.ws.binaryType = 'arraybuffer';
    s.ws.onerror   = function(){ s.con=0; s.err=1; };
    s.ws.onclose   = function(){ s.con=0; s.err=1; };
    s.ws.onopen    = function(){ s.con=1; };
    s.ws.onmessage = function(ev){
        if (typeof ev.data === 'string') return;     // ignore text control frames
        s.inq.push(new Uint8Array(ev.data));          // copy out; C reads next frame
    };
    return _emscriptenfte_handle_alloc(s);
},
emscriptenfte_trunk_ready : function(sockid)         // 1 = connected, 0 = pending, -1 = dead
{
    var s = FTEH.h[sockid];
    if (s === undefined) return -1;
    if (s.err) return -1;
    return s.con ? 1 : 0;
},
emscriptenfte_trunk_send : function(sockid, data, len)   // returns len / 0 (clogged|pending) / -1 (dead)
{
    var s = FTEH.h[sockid];
    if (s === undefined) return -1;
    if (s.err) return -1;
    if (s.con == 0) return 0;
    if (len == 0) return 0;
    s.ws.send(HEAPU8.subarray(data, data+len));           // zero-copy view, same as ws_send
    return len;
},
emscriptenfte_trunk_recv : function(sockid, data, len)   // one frame/call; >0 bytes, 0 empty, -1 dead
{
    var s = FTEH.h[sockid];
    if (s === undefined) return -1;
    var inp = s.inq.shift();
    if (inp) {
        if (inp.length > len) inp.length = len;           // NOTE: truncation; trunk frames are <= MTU+2
        HEAPU8.set(inp, data);
        return inp.length;
    }
    return s.err ? -1 : 0;
},
emscriptenfte_trunk_close : function(sockid)
{
    var s = FTEH.h[sockid];
    if (s === undefined) return -1;
    if (s.ws != null) { s.ws.close(); s.ws = null; }
    delete FTEH.h[sockid];
    return 0;
},
```

C declarations to add to `web/ftejslib.h` (next to the WS block at lines 22‑27):

```c
//trunk relay transport (Nexus /connect). WS now; WebTransport later behind the same C API.
int  emscriptenfte_trunk_open (const char *connecturl); //ws(s)://host/connect ; returns handle or -1
int  emscriptenfte_trunk_ready(int sockid);             //1 ready, 0 connecting, -1 dead
int  emscriptenfte_trunk_send (int sockid, const void *data, int len); //len / 0 / -1
int  emscriptenfte_trunk_recv (int sockid, void *data, int len);       //one frame; >0 / 0 / -1
void emscriptenfte_trunk_close(int sockid);
```

**Queue / Asyncify considerations:**
- No Asyncify. The C side must treat `open` as non‑blocking: `emscriptenfte_trunk_open` returns a
  handle immediately while `con==0`. C polls `emscriptenfte_trunk_ready()` each frame and must hold
  outbound packets (return `NETERR_CLOGGED`) until ready. This matches what `emscriptenfte_ws_send`
  already does (returns 0 while `con==0`, `web/ftejslib.js:1275‑1276`) and how
  `FTENET_WebSocket_SendPacket` maps `r==0 → NETERR_CLOGGED` (`common/net_wins.c:8822‑8823`).
- The connectionless `getchallenge` resend logic (`CL_CheckForResend`) already retransmits, so a few
  early `NETERR_CLOGGED` returns during WS open are harmless — the handshake simply retries.
- One frame per `recv` call; C loops until 0 (the `NET_ReadPackets`/`GetPacket` loop already does
  this, `common/net_wins.c:9465`).

---

## 4. Connect / send / recv data flow

New C connection type in `common/net_wins.c` (under `#ifdef FTE_TARGET_WEB`, modelled on
`ftenet_websocket_connection_t` at `8765‑8781` and its methods at `8798‑8827`, `9269‑9315`):

```c
#define TRUNK_PORT_HDR  2
#define TRUNK_CTL_PORT  0
#define TRUNK_MAGIC_NQIP "NQIP"
typedef struct {
    ftenet_generic_connection_t generic;
    netadr_t remoteadr;          // {NA_WEBSOCKET, NP_TRUNK, websocketurl, port=server udp port}
    int      datasock;           // handle from emscriptenfte_trunk_open
    qboolean failed;
    unsigned short serverport;   // dest port for outbound frames (host order); default 26000
    byte     virtualip[4];       // from NQIP, diagnostic only
    qboolean virtualip_set;
} ftenet_trunk_connection_t;
```

### Trace

1. **User `connect trunk://host/connect` (or server‑browser join).**
   `CL_Connect_f` → `CL_BeginServerConnect` (`client/cl_main.c:1869`, `1601`) →
   `NET_StringToAdr2(cls.servername, …)` (`client/cl_main.c:1286`).
2. **StringToAdr.** New `trunk(s)://` arm (§1) sets
   `adr.type=NA_WEBSOCKET, adr.prot=NP_TRUNK, adr.address.websocketurl="trunk://host/connect"`,
   optional `adr.port` from `#port`. Returns 1.
3. **Open trunk.** `CL_CheckForResend`/`NET_EnsureRoute` → `FTENET_AddToCollection` →
   establish dispatch (`net_wins.c:3525‑3530`) routes `NP_TRUNK` to
   `FTENET_Trunk_EstablishConnection`. That function:
   - resolves the `ws(s)://host/connect` URL from `websocketurl` (strip the `trunk` scheme, choose
     `ws`/`wss` by a `trunks://` variant or page protocol),
   - `datasock = emscriptenfte_trunk_open(url);`
   - sets `generic.GetPacket=FTENET_Trunk_GetPacket`, `generic.SendPacket=FTENET_Trunk_SendPacket`,
     `generic.Close=FTENET_Trunk_Close`, `generic.addrtype[0]=NA_WEBSOCKET`,
     `generic.prot=NP_TRUNK`, `serverport = adr.port?ntohs(adr.port):26000`,
     `remoteadr=adr; remoteadr.port=0;` (FTE‑facing identity), returns the connection.
4. **NQIP received.** First frames after the WS opens include the port‑0 `"NQIP"`+VirtualIP.
   `FTENET_Trunk_GetPacket` drains via `emscriptenfte_trunk_recv`; on `src_port==0` it runs the
   control demux: if 8 bytes and first 4 == `"NQIP"`, store `virtualip`, set `virtualip_set`,
   **return false for that frame** (consumed, FTE sees nothing) and continue draining. Any other
   port‑0 frame is also consumed (future relay control). Only non‑zero‑port frames are surfaced.
5. **Handshake datagrams.** FTE sends connectionless `\xff\xff\xff\xffgetchallenge …`
   (`client/cl_main.c:1500‑1557`). `FTENET_Trunk_SendPacket` prepends
   `[serverport>>8][serverport&0xff]` and calls `emscriptenfte_trunk_send(datasock, frame, len+2)`.
   The challenge reply comes back on the server's source port (non‑zero); trunk strips the prefix and
   delivers it as `net_from=remoteadr`. The QW challenge→connect→`svc_serverdata` sequence proceeds
   exactly as over UDP.
6. **In‑game packets.** Steady‑state netchan datagrams flow the same way: prefix on send, strip on
   recv. `NQNetChan_Process`/QW netchan parse `net_message` unchanged.

### Methods (sketch)

```c
static qboolean FTENET_Trunk_GetPacket(ftenet_generic_connection_t *g){
    ftenet_trunk_connection_t *t=(void*)g; byte frame[MAX_UDP_PACKET]; int n;
    for(;;){
        n = emscriptenfte_trunk_recv(t->datasock, frame, sizeof(frame));
        if (n < 0){ t->failed=true; return false; }
        if (n == 0) return false;                 // queue drained this frame
        if (n < TRUNK_PORT_HDR) continue;         // runt
        {
            int srcport=(frame[0]<<8)|frame[1];
            byte *pl=frame+TRUNK_PORT_HDR; int pll=n-TRUNK_PORT_HDR;
            if (srcport==TRUNK_CTL_PORT){          // control channel
                if (pll==8 && !memcmp(pl,TRUNK_MAGIC_NQIP,4)){
                    memcpy(t->virtualip, pl+4, 4); t->virtualip_set=true;
                }
                continue;                          // consume; never surface to FTE
            }
            memcpy(net_message_buffer, pl, pll);
            net_message.cursize = pll;
            net_from = t->remoteadr;
            return true;                           // one game datagram delivered
        }
    }
}
static neterr_t FTENET_Trunk_SendPacket(ftenet_generic_connection_t *g,int len,const void*data,netadr_t*to){
    ftenet_trunk_connection_t *t=(void*)g; byte frame[MAX_UDP_PACKET]; int r;
    if (t->failed) return NETERR_DISCONNECTED;
    if (!NET_CompareAdr(to,&t->remoteadr)) return NETERR_NOROUTE;
    if (len+TRUNK_PORT_HDR > (int)sizeof(frame)) return NETERR_MTU;
    frame[0]=(t->serverport>>8)&0xff; frame[1]=t->serverport&0xff;
    memcpy(frame+TRUNK_PORT_HDR, data, len);
    r = emscriptenfte_trunk_send(t->datasock, frame, len+TRUNK_PORT_HDR);
    if (r<0) return NETERR_DISCONNECTED;
    if (r==0 && len) return NETERR_CLOGGED;        // not connected yet → caller resends
    return NETERR_SENT;
}
static void FTENET_Trunk_Close(ftenet_generic_connection_t *g){
    ftenet_trunk_connection_t *t=(void*)g;
    if (t->datasock!=INVALID_SOCKET) emscriptenfte_trunk_close(t->datasock);
}
```

### FTE functions that need trunk awareness
- `netproto_t` enum (`net.h`), URI table, `NET_StringToAdr2`, establish dispatch, two `AdrToString`
  switches, TLS predicate — all listed in §1.
- **No change** needed to `NetadrToSockadr`/`SockadrToNetadr`/`NET_CompareAdr`/`NET_CompareBaseAdr`/
  `NET_ReadPackets`/`NET_SendPacketCol` — they treat `NA_WEBSOCKET` opaquely and route by `connum`
  (`net_wins.c:9461‑9484`, `9540‑9560`), which the establish step already assigns.

---

## 5. Build wiring

- **New source:** put the trunk connection type inside `common/net_wins.c` (it must see the static
  `ftenet_*` helpers and the `emscriptenfte_trunk_*` externs; the existing WS/RTC web code already
  lives there under `#ifdef FTE_TARGET_WEB`). No new `.c` file/object is required, so no
  `COMMON_OBJS` edit. (If we prefer a separate file, add `common/net_trunk.c` and append to
  `COMMON_OBJS` near `Makefile:1878`, but co‑locating is lower‑risk and matches FTE style.)
- **JS:** the new `emscriptenfte_trunk_*` entries go in the single `mergeInto` block in
  `web/ftejslib.js`; they are linked by the existing `--js-library web/ftejslib.js`
  (`Makefile:1888`). The `Makefile` already lists `web/ftejslib.js`/`web/prejs.js` as link
  prerequisites (`Makefile:2345`), so they trigger a relink automatically. **No Makefile change is
  strictly required.** `ERROR_ON_UNDEFINED_SYMBOLS=1` means the C externs in `ftejslib.h` and the JS
  keys must match exactly — verify spelling.
- **Header:** add the 5 externs to `web/ftejslib.h`.
- **Delivery:** as a git patch under this repo's `patches/`, named e.g.
  `patches/fteqw-trunk.patch`, generated against the FTE tree with the same `a/engine/… b/engine/…`
  prefix as `patches/fteqw-menu.patch` (confirmed format: `diff --git a/engine/client/cl_main.c …`).
  The patch touches `engine/common/net.h`, `engine/common/net_wins.c`, `engine/web/ftejslib.js`,
  `engine/web/ftejslib.h`. No separate source files need shipping if we co‑locate in `net_wins.c`.

---

## 6. Server side

- Launch `fteqw-sv` as a **plain UDP Quake server**: `set sv_port 26000` (or whatever port the trunk
  layer targets in §2). That is the only requirement — trunk delivers ordinary UDP datagrams to
  localhost, and Nexus binds the per‑session source to the VirtualIP.
- `sv_port_tcp` / `net_enable_websockets` / any TCP/WS listener on the server are **NOT needed** when
  using trunk. The browser no longer speaks WS *to fteqw‑sv*; it speaks WS to *Nexus*, which speaks
  UDP to fteqw‑sv. The server sees only UDP. (Those server‑side WS options are for FTE's *native*
  browser path, which trunk replaces.)
- Server‑side implication: because each browser session arrives from a distinct `127.x.x.x`
  VirtualIP, normal per‑IP rate limits / `sv_fastconnect`‑style logic see distinct sources, which is
  correct. If Nexus and fteqw‑sv share the host, ensure `sv_port` binds `127.0.0.1`/`0.0.0.0` so
  Nexus's localhost UDP reaches it. No FTE server code changes.

---

## 7. WS first, WebTransport later

The C API (`emscriptenfte_trunk_open/ready/send/recv/close`) is the abstraction boundary; the C side
(`FTENET_Trunk_*`, framing, NQIP) is transport‑agnostic. Only `web/ftejslib.js` changes between
variants:

| Concern | WS variant (v1) | WebTransport variant (later) |
|---|---|---|
| JS object | `{ws, inq, err, con}` reusing `new WebSocket(url,'fteqw-trunk')` | `{session, writer, recvQueue, opened, closed}` over `new WebTransport(url)` + `session.datagrams` |
| Reliability | reliable, ordered (TCP under WS) | **unreliable, unordered datagrams** — matches UDP semantics better |
| Receive | `onmessage` → `inq.push` | async reader loop over `session.datagrams.readable` → queue (model: NexQuake `net_wt.c` `WT_JsStart`) |
| Send | `ws.send(view)` | `writer.write(view)`; **drop frames > `maxDatagramSize`** as packet loss (NexQuake `WT_JsSend` pattern), return `len` so C treats it as sent |
| `ready` | `s.con` | `session.ready` resolved → `opened` |
| Framing/NQIP | unchanged | unchanged |

Design accommodation already in place: `emscriptenfte_trunk_open` takes a full resolved URL and
returns a handle; `ready` is a tri‑state poll; `recv` is one‑frame‑per‑call. A WT implementation
fills the same five functions with the datagram queue model. The only behavioural caveat is that WT
is lossy — but Quake's netchan already tolerates loss, and the WS variant's reliability is merely a
stricter guarantee, so switching does not change FTE's logic. (Note FTE is not Asyncify, so the WT
reader loop must enqueue and let C poll — do **not** copy NexQuake's `tick()`+`emscripten_sleep`
blocking drain; instead drain inside `recv`/`GetPacket` per frame.)

---

## 8. Risks / unknowns (ordered by risk)

1. **WS subprotocol negotiation with Nexus `/connect`.** We propose subprotocol `'fteqw-trunk'`;
   Nexus must accept it (or we pass an empty/agreed value). If Nexus rejects the subprotocol the WS
   handshake fails silently (→ `s.err`). **Needs a build+connect cycle to confirm the exact
   subprotocol string Nexus expects.** Check `nexus/` server code before finalizing.
2. **Server port discovery.** v1 hardcodes 26000. If a deployment runs fteqw‑sv on another port, or
   Nexus expects port 0 to mean "the session's default server," the prefix value is wrong. **Verify
   against Nexus's demux: does it route by the 2‑byte dest port to a configured upstream, or ignore
   it for single‑server sessions?** This determines whether §2 option‑1 (fixed) suffices.
3. **NQIP timing vs first outbound packet.** FTE may emit `getchallenge` before the NQIP frame
   arrives. That is fine *if* Nexus routes outbound frames regardless of whether the client has
   consumed NQIP (it should, since the server‑side bind is independent). **Confirm Nexus does not
   gate outbound on the client ACKing NQIP.** If it does, hold sends until `virtualip_set` (add that
   gate to `FTENET_Trunk_SendPacket`).
4. **MTU / frame size.** `net_message_buffer` is `MAX_OVERALLMSGLEN`; `MAX_UDP_PACKET` is 8192
   (`net.h:121‑122`). The +2 prefix must fit, and `emscriptenfte_trunk_recv` truncates silently on
   overflow (§0.4). Quake datagrams are well under this, but **verify Nexus never coalesces multiple
   UDP datagrams into one WS message** — the recv path assumes one Quake datagram per frame.
5. **`URISCHEME_NEEDSRESOURCE` parsing of `/connect`.** The scheme table flag forces a path component
   (`net_wins.c:1625`). Confirm `trunk://host/connect` parses with the path retained in
   `websocketurl` (the `ws://` model at `net_wins.c:1767‑1775` keeps the whole string, so this should
   work, but validate the `/connect` suffix survives StringToAdr).
6. **Reconnect / handle reuse.** The WS handle is freed in `trunk_close` via `delete FTEH.h[sockid]`.
   On disconnect+reconnect, FTE creates a fresh connection (the establish path Z_Mallocs a new
   struct). Low risk, but exercise a reconnect once.
7. **`cl_web_connect_host` interaction.** The repo's `fteqw-menu.patch` adds a
   `cl_web_connect_host` allow‑list checking `ws://`/`wss://` prefixes (`client/cl_main.c`, in that
   patch). If set, it may reject `trunk://`. **Extend that allow‑list check to accept `trunk(s)://`**
   or the trunk connect will be blocked when the cvar is configured. Low‑effort, but easy to miss.

---

## Appendix — grounding quotes

URI scheme table, web arm (`common/net_wins.c:1630‑1635`):
```c
#elif defined(HAVE_WEBSOCKCL)
    {"ws://",   NP_WS,      NA_WEBSOCKET, URISCHEME_NEEDSRESOURCE},
    {"wss://",  NP_WSS,     NA_WEBSOCKET, URISCHEME_NEEDSRESOURCE},
    {"tcp://",  NP_WS,      NA_WEBSOCKET, URISCHEME_NEEDSRESOURCE},  //fake it
    {"tls://",  NP_WSS,     NA_WEBSOCKET, URISCHEME_NEEDSRESOURCE},  //fake it
#endif
```

Establish dispatch (`common/net_wins.c:3525‑3529`):
```c
#ifdef HAVE_WEBSOCKCL
    if (adr->prot == NP_WS && adr->type == NA_WEBSOCKET)  establish = FTENET_WebSocket_EstablishConnection; else
    if (adr->prot == NP_WSS && adr->type == NA_WEBSOCKET) establish = FTENET_WebSocket_EstablishConnection; else
    if (adr->prot == NP_RTC_TCP)                          establish = FTENET_WebRTC_EstablishConnection; else
    if (adr->prot == NP_RTC_TLS)                          establish = FTENET_WebRTC_EstablishConnection; else
#endif
```

Existing WS recv/send the trunk mirrors (`common/net_wins.c:8798‑8826`):
```c
static qboolean FTENET_WebSocket_GetPacket(ftenet_generic_connection_t *gcon){
    ftenet_websocket_connection_t *wsc = (void*)gcon;
    net_message.cursize = emscriptenfte_ws_recv(wsc->datasock, net_message_buffer, sizeof(net_message_buffer));
    if (net_message.cursize > 0){ net_from = wsc->remoteadr; return true; }
    if ((int)net_message.cursize < 0) wsc->failed = true;
    net_message.cursize = 0; return false;
}
static neterr_t FTENET_WebSocket_SendPacket(ftenet_generic_connection_t *gcon,int length,const void*data,netadr_t*to){
    ftenet_websocket_connection_t *wsc = (void*)gcon;
    if (wsc->failed) return NETERR_DISCONNECTED;
    if (NET_CompareAdr(to, &wsc->remoteadr)){
        int r = emscriptenfte_ws_send(wsc->datasock, data, length);
        if (r < 0) return NETERR_DISCONNECTED;
        if (r == 0 && length) return NETERR_CLOGGED;
        return NETERR_SENT;
    }
    return NETERR_NOROUTE;
}
```

Existing WS JS connect (the trunk JS mirrors) (`web/ftejslib.js:1210‑1232`):
```js
emscriptenfte_ws_connect : function(brokerurl, protocolname){
    var _url = UTF8ToString(brokerurl);
    var _protocol = UTF8ToString(protocolname);
    var s = {ws:null, inq:[], err:0, con:0};
    try { s.ws = new WebSocket(_url, _protocol); } catch(err) { console.log(err); }
    if (s.ws === undefined) return -1;
    if (s.ws == null) return -1;
    s.ws.binaryType = 'arraybuffer';
    s.ws.onerror = function(event) {s.con = 0; s.err = 1;};
    s.ws.onclose = function(event) {s.con = 0; s.err = 1;};
    s.ws.onopen = function(event) {s.con = 1;};
    s.ws.onmessage = function(event){ s.inq.push(new Uint8Array(event.data)); };
    return _emscriptenfte_handle_alloc(s);
},
```

NexQuake framing this design reimplements C‑side (`dev-NexQuake/src/client/net_nqchan.c:296‑308`,
`238‑240`): 2‑byte BE port prepended on send, read+stripped on recv; NQIP = port‑0, 8‑byte,
`"NQIP"`+4‑byte VirtualIP, consumed not forwarded.
