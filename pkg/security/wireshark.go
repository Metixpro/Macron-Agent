package security

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// WiresharkParams configures PCAP/network packet inspection via tshark or
// Macron's built-in pure-Go PCAP analyzer fallback.
type WiresharkParams struct {
	Action     string // summary, packets, conversations, dns, http, tls, extract_ips, status
	Path       string // .pcap or .pcapng file path
	Filter     string // Wireshark display filter (e.g. "dns || http.request")
	Limit      int
	TimeoutSec int
}

// RunWireshark inspects PCAP/PCAPNG captures using tshark when installed, or
// falls back to a built-in pure-Go classic PCAP parser for zero-dependency analysis.
func (e Env) RunWireshark(ctx context.Context, sessionID string, p WiresharkParams) (string, error) {
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		action = "summary"
	}
	tshark, hasTshark := FindBinary("tshark", "tshark.exe")
	if action == "status" {
		if hasTshark {
			out, _, _, _ := e.RunBin(ctx, sessionID, 10*time.Second, tshark, "--version")
			first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
			return fmt.Sprintf("Wireshark/tshark ready: %s (%s)", first, tshark), nil
		}
		return "tshark is not installed (install wireshark/tshark for full display-filter & protocol dissection). Built-in pure-Go PCAP parser is active for .pcap summary/IP/port inspection.", nil
	}

	resolved, err := e.ResolvePath(sessionID, p.Path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolved); err != nil {
		return "", fmt.Errorf("capture file not found: %s", resolved)
	}

	limit := p.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 150 {
		limit = 150
	}
	timeout := ClampTimeout(p.TimeoutSec, 60)

	if !hasTshark {
		return parseClassicPCAP(resolved, action, limit)
	}

	var args []string
	switch action {
	case "summary":
		args = []string{"-r", resolved, "-q", "-z", "io,phs"}
	case "conversations":
		args = []string{"-r", resolved, "-q", "-z", "conv,ip"}
	case "packets":
		args = []string{"-r", resolved, "-c", fmt.Sprint(limit)}
		if f := strings.TrimSpace(p.Filter); f != "" {
			args = append(args, "-Y", f)
		}
	case "dns":
		flt := "dns.qry.name"
		if f := strings.TrimSpace(p.Filter); f != "" {
			flt = "(" + flt + ") && (" + f + ")"
		}
		args = []string{"-r", resolved, "-Y", flt, "-T", "fields", "-e", "ip.src", "-e", "dns.qry.name", "-e", "dns.a", "-c", fmt.Sprint(limit)}
	case "http":
		flt := "http.request"
		if f := strings.TrimSpace(p.Filter); f != "" {
			flt = "(" + flt + ") && (" + f + ")"
		}
		args = []string{"-r", resolved, "-Y", flt, "-T", "fields", "-e", "ip.src", "-e", "http.request.method", "-e", "http.host", "-e", "http.request.uri", "-c", fmt.Sprint(limit)}
	case "tls":
		flt := "tls.handshake.extensions_server_name"
		if f := strings.TrimSpace(p.Filter); f != "" {
			flt = "(" + flt + ") && (" + f + ")"
		}
		args = []string{"-r", resolved, "-Y", flt, "-T", "fields", "-e", "ip.src", "-e", "ip.dst", "-e", "tls.handshake.extensions_server_name", "-c", fmt.Sprint(limit)}
	case "extract_ips":
		args = []string{"-r", resolved, "-T", "fields", "-e", "ip.src", "-e", "ip.dst"}
		if f := strings.TrimSpace(p.Filter); f != "" {
			args = append(args, "-Y", f)
		}
	default:
		return "", fmt.Errorf("unknown wireshark action %q (use: summary, packets, conversations, dns, http, tls, extract_ips, status)", action)
	}

	stdout, stderr, code, err := e.RunBin(ctx, sessionID, timeout, tshark, args...)
	if err != nil {
		return "", fmt.Errorf("tshark error: %w", err)
	}
	if code != 0 && strings.TrimSpace(stdout) == "" {
		return "", fmt.Errorf("tshark exited %d: %s", code, truncateLine(stderr, 260))
	}

	if action == "extract_ips" {
		counts := map[string]int{}
		for _, line := range strings.Split(stdout, "\n") {
			for _, ip := range strings.Fields(line) {
				if net.ParseIP(ip) != nil {
					counts[ip]++
				}
			}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Wireshark Unique IPs (%s, total=%d)\n", resolved, len(counts))
		for _, kv := range TopCounts(counts, limit) {
			fmt.Fprintf(&sb, "- %s (%d packets)\n", kv.Key, kv.Count)
		}
		return CapOutput(sb.String()), nil
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return fmt.Sprintf("Wireshark (%s): no matching packets in %s", action, resolved), nil
	}
	return CapOutput(fmt.Sprintf("Wireshark %s (%s)\n%s", action, resolved, out)), nil
}

// parseClassicPCAP is a zero-dependency fallback parser for standard libpcap
// captures (magic 0xa1b2c3d4 / 0xd4c3b2a1) that extracts packet counts, protocols,
// top IP talkers, and top TCP/UDP ports even without tshark installed.
func parseClassicPCAP(path, action string, limit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var gh [24]byte
	if _, err := io.ReadFull(f, gh[:]); err != nil {
		return "", fmt.Errorf("not a valid PCAP file (too short): %w", err)
	}
	var order binary.ByteOrder
	switch {
	case gh[0] == 0xd4 && gh[1] == 0xc3 && gh[2] == 0xb2 && gh[3] == 0xa1:
		order = binary.LittleEndian
	case gh[0] == 0xa1 && gh[1] == 0xb2 && gh[2] == 0xc3 && gh[3] == 0xd4:
		order = binary.BigEndian
	case gh[0] == 0x0a && gh[1] == 0x0d && gh[2] == 0x0d && gh[3] == 0x0a:
		return "PCAPNG file detected, but tshark is not installed. Install Wireshark/tshark to dissect .pcapng captures.", nil
	default:
		return "", fmt.Errorf("unrecognized capture header (install tshark for full format support)")
	}
	linkType := order.Uint32(gh[20:24])

	totalPkts := 0
	var totalBytes uint64
	protos := map[string]int{}
	ips := map[string]int{}
	convs := map[string]int{}
	ports := map[string]int{}
	var sampleLines []string

	var ph [16]byte
	buf := make([]byte, 65536)
	for {
		if _, err := io.ReadFull(f, ph[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return "", err
		}
		tsSec := order.Uint32(ph[0:4])
		inclLen := order.Uint32(ph[8:12])
		if inclLen > 16<<20 {
			return "", fmt.Errorf("corrupt or unsupported PCAP packet length (%d bytes > 16MB limit)", inclLen)
		}
		if inclLen > uint32(len(buf)) {
			buf = make([]byte, inclLen)
		}
		if _, err := io.ReadFull(f, buf[:inclLen]); err != nil {
			break
		}
		totalPkts++
		totalBytes += uint64(inclLen)

		pkt := buf[:inclLen]
		ipOff := -1
		if linkType == 1 && len(pkt) >= 14 { // Ethernet
			ethType := binary.BigEndian.Uint16(pkt[12:14])
			if ethType == 0x0800 {
				ipOff = 14
			} else if ethType == 0x0806 {
				protos["ARP"]++
			} else if ethType == 0x86dd {
				protos["IPv6"]++
			}
		} else if linkType == 101 && len(pkt) >= 20 { // Raw IP
			if pkt[0]>>4 == 4 {
				ipOff = 0
			}
		}

		if ipOff >= 0 && len(pkt) >= ipOff+20 {
			ihl := int(pkt[ipOff]&0x0f) * 4
			if ihl >= 20 && len(pkt) >= ipOff+ihl {
				protoNum := pkt[ipOff+9]
				src := net.IP(pkt[ipOff+12 : ipOff+16]).String()
				dst := net.IP(pkt[ipOff+16 : ipOff+20]).String()
				ips[src]++
				ips[dst]++
				protoName := fmt.Sprintf("IP/%d", protoNum)
				portNote := ""
				switch protoNum {
				case 6:
					protoName = "TCP"
					if len(pkt) >= ipOff+ihl+4 {
						sp := binary.BigEndian.Uint16(pkt[ipOff+ihl : ipOff+ihl+2])
						dp := binary.BigEndian.Uint16(pkt[ipOff+ihl+2 : ipOff+ihl+4])
						ports[fmt.Sprintf("TCP/%d", dp)]++
						portNote = fmt.Sprintf(":%d->:%d", sp, dp)
					}
				case 17:
					protoName = "UDP"
					if len(pkt) >= ipOff+ihl+4 {
						sp := binary.BigEndian.Uint16(pkt[ipOff+ihl : ipOff+ihl+2])
						dp := binary.BigEndian.Uint16(pkt[ipOff+ihl+2 : ipOff+ihl+4])
						ports[fmt.Sprintf("UDP/%d", dp)]++
						portNote = fmt.Sprintf(":%d->:%d", sp, dp)
					}
				case 1:
					protoName = "ICMP"
				}
				protos[protoName]++
				convs[src+" -> "+dst+" ("+protoName+")"]++
				if len(sampleLines) < limit {
					sampleLines = append(sampleLines, fmt.Sprintf("#%d %s %s %s->%s%s (%dB)",
						totalPkts, time.Unix(int64(tsSec), 0).UTC().Format("15:04:05"), protoName, src, dst, portNote, inclLen))
				}
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Wireshark Built-in PCAP Summary (%s, tshark not installed)\nPackets: %d | Bytes: %d\n", path, totalPkts, totalBytes)
	if top := TopCounts(protos, 8); len(top) > 0 {
		parts := make([]string, 0, len(top))
		for _, kv := range top {
			parts = append(parts, fmt.Sprintf("%s=%d", kv.Key, kv.Count))
		}
		sb.WriteString("Protocols: " + strings.Join(parts, ", ") + "\n")
	}
	if action == "extract_ips" || action == "summary" {
		if top := TopCounts(ips, min(limit, 10)); len(top) > 0 {
			sb.WriteString("\nTop IPs:\n")
			for _, kv := range top {
				fmt.Fprintf(&sb, "- %s (%d pkts)\n", kv.Key, kv.Count)
			}
		}
	}
	if action == "conversations" || action == "summary" {
		if top := TopCounts(convs, min(limit, 10)); len(top) > 0 {
			sb.WriteString("\nTop Conversations:\n")
			for _, kv := range top {
				fmt.Fprintf(&sb, "- %s (%d pkts)\n", kv.Key, kv.Count)
			}
		}
	}
	if top := TopCounts(ports, 8); len(top) > 0 && action == "summary" {
		parts := make([]string, 0, len(top))
		for _, kv := range top {
			parts = append(parts, fmt.Sprintf("%s(%d)", kv.Key, kv.Count))
		}
		sb.WriteString("Top Dest Ports: " + strings.Join(parts, ", ") + "\n")
	}
	if action == "packets" && len(sampleLines) > 0 {
		sb.WriteString("\nPackets:\n" + strings.Join(sampleLines, "\n") + "\n")
	}
	return CapOutput(sb.String()), nil
}
