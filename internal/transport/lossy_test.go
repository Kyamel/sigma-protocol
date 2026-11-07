package transport

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/PedroAlvesPaula/sigma-protocol/internal/wire"
)

// drain lê de c até não chegar nada por idle e devolve os datagramas lidos.
func drain(t *testing.T, c PacketConn, idle time.Duration) [][]byte {
	t.Helper()
	var out [][]byte
	buf := make([]byte, 64*1024)
	for {
		if err := c.SetReadDeadline(time.Now().Add(idle)); err != nil {
			t.Fatal(err)
		}
		n, _, err := c.ReadFrom(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return out
		}
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		out = append(out, append([]byte(nil), buf[:n]...))
	}
}

func seqPacket(i uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, i)
	return b
}

func newLossy(t *testing.T, inner PacketConn, cfg LossyConfig) *LossyConn {
	t.Helper()
	c, err := NewLossyConn(inner, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Critério: com 5% de perda e 100.000 datagramas, a perda medida fica entre
// 4,5% e 5,5%.
func TestLossRate(t *testing.T) {
	const total = 100_000
	a, b := Pipe(total)
	defer b.Close()
	l := newLossy(t, a, LossyConfig{Seed: 42, Send: Impairment{Loss: 0.05}})

	for i := range uint32(total) {
		if _, err := l.WriteTo(seqPacket(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	got := drain(t, b, 50*time.Millisecond)
	loss := 1 - float64(len(got))/total
	t.Logf("perda medida: %.3f%% (%d de %d)", 100*loss, total-len(got), total)
	if loss < 0.045 || loss > 0.055 {
		t.Fatalf("perda medida %.4f fora de [0,045; 0,055]", loss)
	}
	if st := l.Stats().Send; st.Packets != total || int(st.Dropped) != total-len(got) {
		t.Fatalf("estatísticas inconsistentes: %+v", st)
	}
}

// A perda também pode ser aplicada na recepção (útil quando só um lado é
// envolvido, por exemplo sobre um socket UDP real).
func TestRecvLossRate(t *testing.T) {
	const total = 20_000
	a, b := Pipe(total)
	defer a.Close()
	l := newLossy(t, b, LossyConfig{Seed: 3, Recv: Impairment{Loss: 0.5}, QueueSize: total})

	for i := range uint32(total) {
		a.WriteTo(seqPacket(i), nil)
	}
	got := drain(t, l, 100*time.Millisecond)
	ratio := float64(len(got)) / total
	if ratio < 0.48 || ratio > 0.52 {
		t.Fatalf("entregues %.3f, esperado ~0,5", ratio)
	}
	if st := l.Stats(); st.Recv.Packets != total || st.Send.Packets != 0 {
		t.Fatalf("estatísticas: %+v", st)
	}
}

func runScenario(t *testing.T, seed uint64) [][]byte {
	t.Helper()
	a, b := Pipe(10_000)
	defer b.Close()
	l := newLossy(t, a, LossyConfig{
		Seed: seed,
		Send: Impairment{Loss: 0.1, Duplicate: 0.1, Corrupt: 0.1},
	})
	for i := range uint32(2000) {
		l.WriteTo(seqPacket(i), nil)
	}
	return drain(t, b, 50*time.Millisecond)
}

// Critério: mesma semente → mesma sequência de eventos.
func TestDeterministic(t *testing.T) {
	first := runScenario(t, 7)
	second := runScenario(t, 7)
	if !slices.EqualFunc(first, second, slices.Equal) {
		t.Fatal("mesma semente produziu sequências diferentes")
	}
	other := runScenario(t, 8)
	if slices.EqualFunc(first, other, slices.Equal) {
		t.Fatal("sementes diferentes produziram a mesma sequência")
	}
}

// Critério: datagrama corrompido pelo simulador é rejeitado por wire.Decode.
func TestCorruptionRejectedByDecode(t *testing.T) {
	const total = 5000
	a, b := Pipe(total)
	defer b.Close()
	l := newLossy(t, a, LossyConfig{Seed: 11, Send: Impairment{Corrupt: 1}})

	buf := make([]byte, wire.MaxDatagram)
	payload := []byte("bloco de dados de teste")
	for i := range uint32(total) {
		n, err := wire.Encode(buf, wire.Header{Type: wire.TypeData, Seq: i, Window: 32}, payload)
		if err != nil {
			t.Fatal(err)
		}
		l.WriteTo(buf[:n], nil)
	}
	got := drain(t, b, 50*time.Millisecond)
	if len(got) != total {
		t.Fatalf("recebidos %d de %d", len(got), total)
	}
	for i, dg := range got {
		if _, _, err := wire.Decode(dg); err == nil {
			t.Fatalf("datagrama corrompido %d aceito", i)
		}
	}
	if c := l.Stats().Send.Corrupted; c != total {
		t.Fatalf("Corrupted = %d", c)
	}
}

func TestDuplicate(t *testing.T) {
	a, b := Pipe(0)
	defer b.Close()
	l := newLossy(t, a, LossyConfig{Send: Impairment{Duplicate: 1}})
	for i := range uint32(100) {
		l.WriteTo(seqPacket(i), nil)
	}
	if got := drain(t, b, 50*time.Millisecond); len(got) != 200 {
		t.Fatalf("esperadas 200 cópias, recebidas %d", len(got))
	}
}

// Atraso com jitter entrega tudo, depois do atraso mínimo e fora de ordem.
func TestDelayAndReorder(t *testing.T) {
	const total = 300
	a, b := Pipe(0)
	defer b.Close()
	l := newLossy(t, a, LossyConfig{Seed: 5, Send: Impairment{Delay: 20 * time.Millisecond, Jitter: 20 * time.Millisecond}})

	start := time.Now()
	for i := range uint32(total) {
		l.WriteTo(seqPacket(i), nil)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Fatalf("WriteTo bloqueou por %v; o envio não deve dormir", elapsed)
	}

	buf := make([]byte, 16)
	b.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := b.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("primeiro datagrama chegou em %v, antes do atraso", elapsed)
	}
	got := append([][]byte{append([]byte(nil), buf[:n]...)}, drain(t, b, 100*time.Millisecond)...)
	if len(got) != total {
		t.Fatalf("recebidos %d de %d", len(got), total)
	}
	seqs := make([]uint32, len(got))
	for i, p := range got {
		seqs[i] = binary.BigEndian.Uint32(p)
	}
	if slices.IsSorted(seqs) {
		t.Fatal("jitter não produziu reordenação")
	}
	slices.Sort(seqs)
	for i, s := range seqs {
		if s != uint32(i) {
			t.Fatalf("faltou ou repetiu datagrama: posição %d = %d", i, s)
		}
	}
}

func TestCloseCancelsPendingDeliveries(t *testing.T) {
	a, b := Pipe(0)
	defer b.Close()
	l, err := NewLossyConn(a, LossyConfig{Send: Impairment{Delay: 30 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	l.WriteTo([]byte("x"), nil)
	l.Close()
	if got := drain(t, b, 80*time.Millisecond); len(got) != 0 {
		t.Fatalf("entrega após Close: %d datagramas", len(got))
	}
	if _, err := l.WriteTo([]byte("y"), nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("WriteTo após Close: %v", err)
	}
	if _, _, err := l.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ReadFrom após Close: %v", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	a, b := Pipe(0)
	defer a.Close()
	defer b.Close()
	bad := []LossyConfig{
		{Send: Impairment{Loss: 1.5}},
		{Recv: Impairment{Duplicate: -0.1}},
		{Send: Impairment{Delay: -time.Second}},
	}
	for _, cfg := range bad {
		if _, err := NewLossyConn(a, cfg); err == nil {
			t.Fatalf("configuração inválida aceita: %+v", cfg)
		}
	}
}

// LossyConn sobre UDP real, com falhas nos dois sentidos aplicadas pelo
// mesmo lado.
func TestLossyOverUDP(t *testing.T) {
	ca, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("UDP indisponível: %v", err)
	}
	cb, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		ca.Close()
		t.Skipf("UDP indisponível: %v", err)
	}
	defer cb.Close()
	l := newLossy(t, ca, LossyConfig{Seed: 1, Send: Impairment{Loss: 0.2}, Recv: Impairment{Loss: 0.2}})

	// A leitura acontece em paralelo à escrita para que o buffer do socket
	// receptor não transborde e se confunda com a perda simulada.
	countWhileSending := func(r PacketConn, send func()) int {
		got := make(chan int)
		go func() {
			n := 0
			buf := make([]byte, 64)
			for {
				r.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				if _, _, err := r.ReadFrom(buf); err != nil {
					got <- n
					return
				}
				n++
			}
		}()
		send()
		return <-got
	}

	const total = 2000
	sent := countWhileSending(cb, func() {
		for i := range uint32(total) {
			if _, err := l.WriteTo(seqPacket(i), cb.LocalAddr()); err != nil {
				t.Error(err)
				return
			}
		}
	})
	received := countWhileSending(l, func() {
		for i := range uint32(total) {
			cb.WriteTo(seqPacket(i), l.LocalAddr())
		}
	})
	t.Logf("UDP: enviados %d/%d, recebidos %d/%d", sent, total, received, total)
	for name, n := range map[string]int{"envio": sent, "recepção": received} {
		if r := float64(n) / total; r < 0.75 || r > 0.85 {
			t.Fatalf("%s: entregues %.3f, esperado ~0,8", name, r)
		}
	}
}
