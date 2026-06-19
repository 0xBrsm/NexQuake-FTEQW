package admin

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xBrsm/NexQuake/nexus/internal/access"
	"github.com/0xBrsm/NexQuake/nexus/internal/clients"
)

// --- client.list ------------------------------------------------------------

type ClientListResult struct {
	Clients []clients.Connection `json:"clients"`
}

func clientListHandler(a *Admin, _ access.Client, _ any) (any, error) {
	if err := a.requireRegistry(); err != nil {
		return nil, err
	}
	return ClientListResult{Clients: a.registry.List()}, nil
}

// --- client.info / client.ban -----------------------------------------------

// ClientLookup is the common param shape for client.info and client.ban.
type ClientLookup struct {
	VirtualAddr string `json:"nqip"`
}

func parseClientLookup(raw json.RawMessage) (any, error) {
	p, err := unmarshalParams[ClientLookup](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.VirtualAddr) == "" {
		return nil, fmt.Errorf("nqip is required")
	}
	return p, nil
}

type ClientInfoResult struct {
	Client clients.Connection `json:"client"`
}

func clientInfoHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*ClientLookup)
	if err := a.requireRegistry(); err != nil {
		return nil, err
	}
	c, ok := a.registry.ByVirtualAddr(p.VirtualAddr)
	if !ok {
		return nil, &MethodError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no client with nqip %q", p.VirtualAddr)}
	}
	return ClientInfoResult{Client: c}, nil
}

type ClientBanResult struct {
	VirtualAddr  string   `json:"nqip"`
	SourceIPs    []string `json:"source_ips,omitempty"`
	Disconnected int      `json:"disconnected"`
}

func clientBanHandler(a *Admin, _ access.Client, params any) (any, error) {
	p := params.(*ClientLookup)
	if err := a.requireRegistry(); err != nil {
		return nil, err
	}
	c, ok := a.registry.ByVirtualAddr(p.VirtualAddr)
	if !ok {
		return nil, &MethodError{Code: ErrCodeNotFound, Message: fmt.Sprintf("no active client with nqip %q", p.VirtualAddr)}
	}

	if a.blocker != nil {
		a.blocker.Block(c.SourceIP)
	}
	disconnected := 0
	if c.Disconnect(ClientCommandPayload("quit")) {
		disconnected = 1
	}

	var sourceIPs []string
	if strings.TrimSpace(c.SourceIP) != "" {
		sourceIPs = []string{c.SourceIP}
	}
	return ClientBanResult{
		VirtualAddr:  c.VirtualAddr,
		SourceIPs:    sourceIPs,
		Disconnected: disconnected,
	}, nil
}

