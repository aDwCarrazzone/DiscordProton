package main

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
)

func tcpRow(state uint32, local, remote string, pid uint32) []byte {
	row := make([]byte, tcpRowSize)
	binary.LittleEndian.PutUint32(row, state)
	copy(row[4:8], net.ParseIP(local).To4())
	copy(row[12:16], net.ParseIP(remote).To4())
	binary.LittleEndian.PutUint32(row[20:], pid)
	return row
}

func tcpTable(rows ...[]byte) []byte {
	buf := binary.LittleEndian.AppendUint32(nil, uint32(len(rows)))
	for _, row := range rows {
		buf = append(buf, row...)
	}
	return buf
}

func TestParseTCPTableKeepsOnlyEstablished(t *testing.T) {
	const listen = 2
	buf := tcpTable(
		tcpRow(tcpStateEstablished, "10.2.0.2", "203.0.113.10", 42),
		tcpRow(listen, "0.0.0.0", "0.0.0.0", 42),
	)
	conns := parseTCPTable(buf)
	if len(conns) != 1 {
		t.Fatalf("esperava 1 conexao, veio %d", len(conns))
	}
	c := conns[0]
	if !c.local.Equal(net.ParseIP("10.2.0.2")) || !c.remote.Equal(net.ParseIP("203.0.113.10")) || c.pid != 42 {
		t.Fatalf("conexao lida errado: %+v", c)
	}
}

// Tabela que diz ter mais linhas do que cabem no buffer nao pode ler fora dele.
func TestParseTCPTableStopsAtBufferEnd(t *testing.T) {
	buf := tcpTable(tcpRow(tcpStateEstablished, "10.2.0.2", "203.0.113.10", 42))
	binary.LittleEndian.PutUint32(buf, 5)
	if conns := parseTCPTable(buf); len(conns) != 1 {
		t.Fatalf("esperava 1 conexao, veio %d", len(conns))
	}
}

func TestClassifyRoute(t *testing.T) {
	tunnel := net.ParseIP("10.2.0.2").To4()
	discord := map[uint32]bool{42: true}
	conn := func(local, remote string, pid uint32) tcpConn {
		return tcpConn{local: net.ParseIP(local).To4(), remote: net.ParseIP(remote).To4(), pid: pid}
	}

	cases := []struct {
		name  string
		conns []tcpConn
		want  route
	}{
		{"sem conexao", nil, routeUnknown},
		{"so conexoes locais e do Tailscale", []tcpConn{
			conn("127.0.0.1", "127.0.0.1", 42),
			conn("192.168.0.10", "192.168.0.1", 42),
			conn("100.64.0.5", "100.100.1.2", 42),
		}, routeUnknown},
		{"tudo pelo tunel", []tcpConn{
			conn("10.2.0.2", "203.0.113.10", 42),
			conn("10.2.0.2", "198.51.100.20", 42),
		}, routeTunnel},
		{"uma saindo por casa", []tcpConn{
			conn("10.2.0.2", "203.0.113.10", 42),
			conn("192.168.0.10", "198.51.100.20", 42),
		}, routeOutside},
		{"outro programa saindo por casa nao conta", []tcpConn{
			conn("10.2.0.2", "203.0.113.10", 42),
			conn("192.168.0.10", "198.51.100.20", 7),
		}, routeTunnel},
	}
	for _, tc := range cases {
		if got := classifyRoute(tc.conns, discord, tunnel); got != tc.want {
			t.Errorf("%s: veio %v, esperava %v", tc.name, got, tc.want)
		}
	}
}

// Mostra por onde cada Discord aberto esta saindo agora, sem mexer em nada:
// DISCORDPROTON_REAL_ROUTE=1 go test -run RealRoute -v
func TestRealRoute(t *testing.T) {
	if os.Getenv("DISCORDPROTON_REAL_ROUTE") == "" {
		t.Skip("so roda com DISCORDPROTON_REAL_ROUTE=1")
	}
	names := map[route]string{routeUnknown: "sem conexao ou sem tunel", routeTunnel: "pelo tunel", routeOutside: "por fora do tunel"}
	for _, channel := range channels {
		t.Logf("%s: %s", channel.Friendly, names[currentRoute(channel.Binary)])
	}
}
