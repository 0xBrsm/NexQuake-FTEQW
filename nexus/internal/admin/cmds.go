package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
)

const defaultServerTailLines = 10

// --- Command registry -------------------------------------------------------

// Command describes a single JSON-RPC admin method. Description is the
// shared one-line purpose, used by both rcon.help (in-game rendering, keyed
// off HelpForm) and rpc.discover (structured self-description, keyed off
// Method). Commands with an empty HelpForm are omitted from rcon.help —
// they are not user-typed (e.g. [server.instance.command] is reached via
// `rcon <port> <cmd...>`).
type Command struct {
	Method      string
	HelpForm    string
	Description string
	ParseParams func(raw json.RawMessage) (any, error)
	Handler     func(a *Admin, actor access.Client, params any) (any, error)
}

var adminCommands = []Command{
	{Method: "rcon.help", HelpForm: "help", Description: "show in-game rcon command help",
		ParseParams: parseEmpty, Handler: rconHelpHandler},
	{Method: "rpc.discover", Description: "list all RPC methods with descriptions",
		ParseParams: parseEmpty, Handler: rpcDiscoverHandler},
	{Method: "client.list", HelpForm: "client list", Description: "list active clients",
		ParseParams: parseEmpty, Handler: clientListHandler},
	{Method: "client.info", HelpForm: "client info <nqip>", Description: "detail for one client",
		ParseParams: parseClientLookup, Handler: clientInfoHandler},
	{Method: "client.ban", HelpForm: "client ban <nqip>", Description: "ban client at nqip until Nexus restart",
		ParseParams: parseClientLookup, Handler: clientBanHandler},
	{Method: "server.instance.command", Description: "forward command to a specific instance",
		ParseParams: parseInstanceCommand, Handler: instanceCommandHandler},
	{Method: "server.list", HelpForm: "server list", Description: "list managed servers",
		ParseParams: parseEmpty, Handler: serverListHandler},
	{Method: "server.instances", HelpForm: "server list <idx|all>", Description: "list instances of one or all servers",
		ParseParams: parseServerInstances, Handler: serverInstancesHandler},
	{Method: "server.start", HelpForm: "server start <idx|all>", Description: "start one or all servers",
		ParseParams: parseServerTarget, Handler: serverStartHandler},
	{Method: "server.stop", HelpForm: "server stop <idx|all> [secs]", Description: "stop one or all servers",
		ParseParams: parseServerStop, Handler: serverStopHandler},
	{Method: "server.restart", HelpForm: "server restart <idx|all> [secs]", Description: "restart one or all servers",
		ParseParams: parseServerStop, Handler: serverRestartHandler},
	{Method: "server.remove", HelpForm: "server remove <idx>", Description: "remove a stopped server",
		ParseParams: parseIndexOnly, Handler: serverRemoveHandler},
	{Method: "server.launch", HelpForm: "server launch <binary> [args...]", Description: "launch and register a new server",
		ParseParams: parseServerLaunch, Handler: serverLaunchHandler},
	{Method: "logs.tail", HelpForm: "tail [N]", Description: "last N (default 10) Nexus log lines",
		ParseParams: parseLogsTail, Handler: logsTailHandler},
}

var (
	helpText        string
	discoverMethods []RPCMethodInfo
	commandByMethod map[string]*Command
)

func init() {
	helpText = buildHelpText()
	discoverMethods = buildDiscoverMethods()
	commandByMethod = make(map[string]*Command, len(adminCommands))
	for i := range adminCommands {
		commandByMethod[adminCommands[i].Method] = &adminCommands[i]
	}
}

func lookupCommand(method string) (*Command, bool) {
	cmd, ok := commandByMethod[method]
	return cmd, ok
}

// --- Shared helpers ---------------------------------------------------------

func parseEmpty(_ json.RawMessage) (any, error) { return struct{}{}, nil }

func unmarshalParams[T any](raw json.RawMessage) (*T, error) {
	var p T
	if len(raw) == 0 || string(raw) == "null" {
		return &p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %v", err)
	}
	return &p, nil
}

// ClientCommandPayload builds the control-channel payload for a server→client
// console command: "RCON" + cmd + 0x00. The WASM client recognizes the
// "RCON" prefix and feeds the null-terminated UTF-8 string into its console
// (Cbuf_AddText). The literal must stay in sync with NQ_RCON_MAGIC in
// src/client/net_nqchan.c.
func ClientCommandPayload(cmd string) []byte {
	return []byte("RCON" + cmd + "\x00")
}

// --- logs.tail --------------------------------------------------------------

type LogsTailParams struct {
	Lines int `json:"lines,omitempty"`
}

type LogsTailResult struct {
	Lines []string `json:"lines"`
}

func parseLogsTail(raw json.RawMessage) (any, error) {
	return unmarshalParams[LogsTailParams](raw)
}

func logsTailHandler(a *Admin, _ access.Client, params any) (any, error) {
	if a == nil || a.tailLog == nil {
		return nil, &MethodError{Code: ErrCodeUnavailable, Message: "log tail unavailable"}
	}
	p := params.(*LogsTailParams)
	n := p.Lines
	if n <= 0 {
		n = defaultServerTailLines
	}
	raw := a.tailLog(n)
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.ReplaceAll(line, "\r", "")
		line = strings.TrimRight(line, "\n")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return LogsTailResult{Lines: out}, nil
}

// --- rcon.help --------------------------------------------------------------

type RconHelpResult struct {
	Text string `json:"text"`
}

func rconHelpHandler(_ *Admin, _ access.Client, _ any) (any, error) {
	return RconHelpResult{Text: helpText}, nil
}

func buildHelpText() string {
	formWidth := 0
	for _, c := range adminCommands {
		if c.HelpForm == "" {
			continue
		}
		if n := len(c.HelpForm); n > formWidth {
			formWidth = n
		}
	}

	var b strings.Builder
	b.WriteString("\nNexQuake commands:\n")
	for _, c := range adminCommands {
		if c.HelpForm == "" {
			continue
		}
		fmt.Fprintf(&b, "  rcon %-*s  %s\n", formWidth, c.HelpForm, c.Description)
	}
	b.WriteString("\ninstance console forms (target one server's console):\n")
	b.WriteString("  rcon <port> <cmd...>\n")
	b.WriteString("  rcon <cmd...>             when connected, uses the current listen port\n")
	b.WriteString("\nprefix any admin command with `nexus` while connected (e.g. `rcon nexus server list`).\n\n")
	return b.String()
}

// --- rpc.discover -----------------------------------------------------------

type RPCMethodInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type RPCDiscoverResult struct {
	Methods []RPCMethodInfo `json:"methods"`
}

func rpcDiscoverHandler(_ *Admin, _ access.Client, _ any) (any, error) {
	return RPCDiscoverResult{Methods: discoverMethods}, nil
}

func buildDiscoverMethods() []RPCMethodInfo {
	out := make([]RPCMethodInfo, 0, len(adminCommands))
	for _, c := range adminCommands {
		out = append(out, RPCMethodInfo{Name: c.Method, Description: c.Description})
	}
	return out
}
