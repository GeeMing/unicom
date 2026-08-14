package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"
)

type localIPOption struct {
	IP    string
	Label string
}

func listLocalIPOptions() []localIPOption {
	candidates := make([]localIPOption, 0)
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range interfaces {
			addresses, addressErr := iface.Addrs()
			if addressErr != nil {
				continue
			}
			for _, address := range addresses {
				ip, _, parseErr := net.ParseCIDR(address.String())
				if parseErr != nil || ip.To4() == nil {
					continue
				}
				ipText := ip.To4().String()
				candidates = append(candidates, localIPOption{IP: ipText, Label: formatLocalIPOption(ipText, iface.Name)})
			}
		}
	}
	return buildLocalIPOptions(candidates)
}

func buildLocalIPOptions(candidates []localIPOption) []localIPOption {
	options := []localIPOption{
		{IP: "0.0.0.0", Label: formatLocalIPOption("0.0.0.0", "所有网卡")},
		{IP: "127.0.0.1", Label: formatLocalIPOption("127.0.0.1", "本机回环")},
	}
	seen := map[string]bool{"0.0.0.0": true, "127.0.0.1": true}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Label < candidates[j].Label })
	for _, candidate := range candidates {
		if candidate.IP == "" || seen[candidate.IP] {
			continue
		}
		seen[candidate.IP] = true
		options = append(options, candidate)
	}
	return options
}

func formatLocalIPOption(ip, interfaceName string) string {
	return fmt.Sprintf("%s [%s]", ip, interfaceName)
}

// normalizeEndpointInput cleans text pasted or typed into an IP field. The
// boolean reports whether a non-empty port was found and should be transferred.
func normalizeEndpointInput(input string) (host, port string, hasPort bool) {
	normalized := normalizeIPInput(input)

	if strings.HasPrefix(normalized, "[") {
		if bracket := strings.Index(normalized, "]"); bracket >= 0 {
			remainder := normalized[bracket+1:]
			if strings.HasPrefix(remainder, ":") && len(remainder) > 1 {
				return normalized[:bracket+1], remainder[1:], true
			}
		}
		return normalized, "", false
	}

	if strings.Count(normalized, ":") == 1 {
		separator := strings.IndexByte(normalized, ':')
		if separator < len(normalized)-1 {
			return normalized[:separator], normalized[separator+1:], true
		}
	}
	return normalized, "", false
}

func normalizeIPInput(input string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\uff0e', '\u3002':
			return '.'
		case '\uff1a':
			return ':'
		}
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, input)
}
