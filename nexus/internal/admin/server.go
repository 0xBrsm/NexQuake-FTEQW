package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/orch"
)

// ServerInfo is a point-in-time view of a managed server for RPC responses.
type ServerInfo struct {
	Line          int    `json:"line"`     // 0-based server line index.
	Hostname      string `json:"hostname"` // Server hostname reported by the game server.
	MapName       string `json:"map_name,omitempty"`
	CandidatePort int    `json:"candidate_port,omitempty"`
	ListenPort    int    `json:"listen_port,omitempty"`
	GameDir       string `json:"game_dir,omitempty"`
	Players       int    `json:"players"`
	MaxPlayers    int    `json:"max_players"`
	Instances     int    `json:"instances,omitempty"`
	State         string `json:"state"`
}

func toServerInfo(s orch.ServerSnapshot) ServerInfo {
	return ServerInfo{
		Line:          s.Line,
		Hostname:      s.Hostname,
		MapName:       s.MapName,
		CandidatePort: s.CandidatePort,
		ListenPort:    s.ListenPort,
		GameDir:       s.GameDir,
		Players:       int(s.Players),
		MaxPlayers:    int(s.MaxPlayers),
		Instances:     int(s.Instances),
		State:         s.State,
	}
}

func toServerInfos(snaps []orch.ServerSnapshot) []ServerInfo {
	out := make([]ServerInfo, len(snaps))
	for i, s := range snaps {
		out[i] = toServerInfo(s)
	}
	return out
}

// parseServerTargetToken interprets a lifecycle target string. Accepts a
// positive 1-based server index (as shown in server.list) or the literal
// "all". Returns (index, isAll, error). Exactly one of index>0 or isAll=true
// on success. Hostnames are NOT accepted — servers are not guaranteed to
// carry one, so the registry index is the only stable identifier.
func parseServerTargetToken(target string) (int, bool, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, false, &MethodError{Code: ErrCodeInvalidParams, Message: "target is required"}
	}
	if strings.EqualFold(target, "all") {
		return 0, true, nil
	}
	idx, err := strconv.Atoi(target)
	if err != nil || idx <= 0 {
		return 0, false, &MethodError{Code: ErrCodeInvalidParams, Message: fmt.Sprintf("invalid target %q: expected 1-based server index or \"all\"", target)}
	}
	return idx, false, nil
}

// --- server.list ------------------------------------------------------------

type ServerListResult struct {
	Servers []ServerInfo `json:"servers"`
}

func serverListHandler(a *Admin, _ access.Client, _ any) (any, error) {
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	return ServerListResult{Servers: toServerInfos(a.orch.Snapshots())}, nil
}

// --- server.instances -------------------------------------------------------

type ServerInstancesParams struct {
	Index int `json:"index,omitempty"` // 1-based server index; 0/omitted = all servers
}

// ServerWithInstances is one server with its running instances nested.
type ServerWithInstances struct {
	Index         int          `json:"index"` // 1-based registry index
	Hostname      string       `json:"hostname"`
	GameDir       string       `json:"game_dir,omitempty"`
	State         string       `json:"state"`
	CandidatePort int          `json:"candidate_port,omitempty"`
	Players       int          `json:"players"`
	MaxPlayers    int          `json:"max_players"`
	Instances     []ServerInfo `json:"instances"`
}

type ServerInstancesResult struct {
	Servers []ServerWithInstances `json:"servers"`
}

func parseServerInstances(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[ServerInstancesParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Index < 0 {
		return nil, fmt.Errorf("invalid index %d: must be positive or omitted", p.Index)
	}
	return p, nil
}

func serverInstancesHandler(a *Admin, _ access.Client, params any) (any, error) {
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	p := params.(*ServerInstancesParams)
	servers := toServerInfos(a.orch.Snapshots())
	rawInst, err := a.orch.InstanceSnapshots(p.Index)
	if err != nil {
		return nil, dispatchErr(err)
	}
	instances := toServerInfos(rawInst)

	byLine := make(map[int][]ServerInfo, len(servers))
	for _, b := range instances {
		byLine[b.Line] = append(byLine[b.Line], b)
	}

	out := make([]ServerWithInstances, 0, len(servers))
	for i, s := range servers {
		idx := i + 1
		if p.Index > 0 && idx != p.Index {
			continue
		}
		out = append(out, ServerWithInstances{
			Index:         idx,
			Hostname:      s.Hostname,
			GameDir:       s.GameDir,
			State:         s.State,
			CandidatePort: s.CandidatePort,
			Players:       s.Players,
			MaxPlayers:    s.MaxPlayers,
			Instances:     byLine[s.Line],
		})
	}
	return ServerInstancesResult{Servers: out}, nil
}

// --- server.start / stop / restart ------------------------------------------

type ServerTargetParams struct {
	Target string `json:"target"` // 1-based server index (as a string) or "all"
}

type ServerStopParams struct {
	Target       string `json:"target"` // 1-based server index (as a string) or "all"
	GraceSeconds int    `json:"grace_seconds,omitempty"`
}

type ServerLifecycleResult struct {
	OK bool `json:"ok"`
}

func parseServerTarget(raw json.RawMessage) (any, error) {
	return unmarshalParams[ServerTargetParams](raw)
}

func parseServerStop(raw json.RawMessage) (any, error) {
	return unmarshalParams[ServerStopParams](raw)
}

func serverStartHandler(a *Admin, _ access.Client, params any) (any, error) {
	idx, all, err := parseServerTargetToken(params.(*ServerTargetParams).Target)
	if err != nil {
		return nil, err
	}
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	if all {
		if err := a.orch.StartServersAll(); err != nil {
			return nil, dispatchErr(err)
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if err := a.orch.StartServer(idx); err != nil {
		return nil, dispatchErr(err)
	}
	return ServerLifecycleResult{OK: true}, nil
}

func graceDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 2 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func serverStopHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*ServerStopParams)
	idx, all, err := parseServerTargetToken(p.Target)
	if err != nil {
		return nil, err
	}
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	grace := graceDuration(p.GraceSeconds)
	ctx, cancel := context.WithTimeout(context.Background(), grace+time.Second)
	defer cancel()
	if all {
		if err := a.orch.StopServersAll(ctx, grace); err != nil {
			return nil, dispatchErr(err)
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if err := a.orch.StopServer(ctx, idx, grace); err != nil {
		return nil, dispatchErr(err)
	}
	return ServerLifecycleResult{OK: true}, nil
}

func serverRestartHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*ServerStopParams)
	idx, all, err := parseServerTargetToken(p.Target)
	if err != nil {
		return nil, err
	}
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	grace := graceDuration(p.GraceSeconds)
	ctx, cancel := context.WithTimeout(context.Background(), grace+3*time.Second)
	defer cancel()
	if all {
		if err := a.orch.RestartServersAll(ctx, grace); err != nil {
			return nil, dispatchErr(err)
		}
		return ServerLifecycleResult{OK: true}, nil
	}
	if err := a.orch.RestartServer(ctx, idx, grace); err != nil {
		return nil, dispatchErr(err)
	}
	return ServerLifecycleResult{OK: true}, nil
}

// --- server.remove ----------------------------------------------------------

type IndexParams struct {
	Index int `json:"index"` // 1-based server index
}

type ServerRemoveResult struct {
	Removed bool `json:"removed"`
}

func parseIndexOnly(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[IndexParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Index <= 0 {
		return nil, fmt.Errorf("server index is required (positive 1-based)")
	}
	return p, nil
}

func serverRemoveHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*IndexParams)
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	if err := a.orch.RemoveServer(p.Index); err != nil {
		return nil, dispatchErr(err)
	}
	return ServerRemoveResult{Removed: true}, nil
}

// --- server.launch ----------------------------------------------------------

type ServerLaunchParams struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args,omitempty"`
}

type ServerLaunchResult struct {
	OK bool `json:"ok"`
}

func parseServerLaunch(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[ServerLaunchParams](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Binary) == "" {
		return nil, fmt.Errorf("binary is required")
	}
	return p, nil
}

func serverLaunchHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*ServerLaunchParams)
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	if err := a.orch.LaunchServer(p.Binary, append([]string(nil), p.Args...)); err != nil {
		return nil, dispatchErr(err)
	}
	return ServerLaunchResult{OK: true}, nil
}

// --- server.instance.command ------------------------------------------------

type InstanceCommandParams struct {
	Port int    `json:"port"` // listen port of a live instance
	Cmd  string `json:"cmd"`
}

type InstanceCommandResult struct {
	Reply string `json:"reply"`
}

func parseInstanceCommand(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[InstanceCommandParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Port <= 0 || p.Port > 65535 {
		return nil, fmt.Errorf("port is required (1..65535)")
	}
	if strings.TrimSpace(p.Cmd) == "" {
		return nil, fmt.Errorf("cmd is required")
	}
	return p, nil
}

func instanceCommandHandler(a *Admin, client access.Client, params any) (any, error) {
	p := params.(*InstanceCommandParams)
	if err := a.requireOrch(); err != nil {
		return nil, err
	}
	reply, err := a.orch.DispatchInstanceCmd(p.Port, p.Cmd, client.ID)
	if err != nil {
		return nil, dispatchErr(err)
	}
	return InstanceCommandResult{Reply: reply}, nil
}
