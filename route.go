package main

import (
	"encoding/binary"
	"net"
)

// Por onde as conexoes de um Discord estao saindo de verdade. Estar ativo na lista da
// Proton nao basta: o servico dela as vezes continua com as regras da lista antiga, e o
// Discord sai pela internet de casa mesmo constando na lista.
type route int

const (
	routeUnknown route = iota // ainda sem conexao com a internet para julgar
	routeTunnel
	routeOutside
)

type tcpConn struct {
	local, remote net.IP
	pid           uint32
}

func classifyRoute(conns []tcpConn, pids map[uint32]bool, tunnel net.IP) route {
	result := routeUnknown
	for _, c := range conns {
		if !pids[c.pid] || !internetAddress(c.remote) {
			continue
		}
		if !c.local.Equal(tunnel) {
			return routeOutside
		}
		result = routeTunnel
	}
	return result
}

// Conexao local, da rede de casa ou do Tailscale nao passa pelo tunel e nem deveria.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func internetAddress(ip net.IP) bool {
	return !(ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip))
}

const (
	tcpRowSize          = 24
	tcpStateEstablished = 5
)

// Tabela do GetExtendedTcpTable com TCP_TABLE_OWNER_PID_ALL: a contagem e depois linhas
// de seis uint32 (estado, IP local, porta local, IP remoto, porta remota, processo). Os
// IPs ja vem na ordem da rede, byte a byte.
func parseTCPTable(buf []byte) []tcpConn {
	if len(buf) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buf))
	var conns []tcpConn
	for i := 0; i < count; i++ {
		row := 4 + i*tcpRowSize
		if row+tcpRowSize > len(buf) {
			break
		}
		if binary.LittleEndian.Uint32(buf[row:]) != tcpStateEstablished {
			continue
		}
		conns = append(conns, tcpConn{
			local:  net.IP(append([]byte(nil), buf[row+4:row+8]...)),
			remote: net.IP(append([]byte(nil), buf[row+12:row+16]...)),
			pid:    binary.LittleEndian.Uint32(buf[row+20:]),
		})
	}
	return conns
}
