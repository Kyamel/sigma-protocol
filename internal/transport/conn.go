// Package transport implementa a camada de transporte confiável do Sigma
// Protocol sobre datagramas.
//
// Este arquivo define a abstração de rede usada pelo transporte. Em produção
// ela é satisfeita por *net.UDPConn; nos testes, por Pipe (em memória) e por
// LossyConn (simulador de falhas), que podem ser combinados.
package transport

import (
	"net"
	"os"
	"sync"
	"time"
)

// PacketConn é o subconjunto de net.PacketConn de que o transporte precisa.
type PacketConn interface {
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
	WriteTo(p []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	LocalAddr() net.Addr
	Close() error
}

// O socket UDP real satisfaz a interface sem adaptação.
var _ PacketConn = (*net.UDPConn)(nil)

// DefaultQueueSize é a capacidade padrão, em datagramas, das filas de
// recepção simuladas. Quando a fila enche, novos datagramas são descartados,
// como ocorre com o buffer de recepção de um socket UDP.
const DefaultQueueSize = 4096

// datagram é um datagrama enfileirado para leitura.
type datagram struct {
	data []byte
	addr net.Addr
}

// packetQueue é uma fila limitada de datagramas com semântica de leitura
// semelhante à de um socket: ReadFrom bloqueia até chegar um datagrama, o
// prazo de leitura expirar ou a fila ser fechada.
type packetQueue struct {
	ch     chan datagram
	closed chan struct{}
	once   sync.Once

	mu         sync.Mutex
	err        error         // erro devolvido após o fechamento
	deadline   time.Time     // prazo de leitura; zero = sem prazo
	deadlineCh chan struct{} // fechado quando o prazo muda
}

func newPacketQueue(size int) *packetQueue {
	if size <= 0 {
		size = DefaultQueueSize
	}
	return &packetQueue{
		ch:         make(chan datagram, size),
		closed:     make(chan struct{}),
		deadlineCh: make(chan struct{}),
	}
}

// push enfileira d sem bloquear. Retorna false se a fila estiver cheia ou
// fechada (o datagrama é descartado).
func (q *packetQueue) push(d datagram) bool {
	select {
	case <-q.closed:
		return false
	default:
	}
	select {
	case q.ch <- d:
		return true
	default:
		return false
	}
}

// read copia o próximo datagrama para p. Se p for menor que o datagrama, o
// excedente é descartado, como em UDP.
func (q *packetQueue) read(p []byte) (int, net.Addr, error) {
	for {
		q.mu.Lock()
		deadline, changed := q.deadline, q.deadlineCh
		q.mu.Unlock()

		if q.isClosed() {
			return 0, nil, q.closeErr()
		}

		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(wait)
			timeout = timer.C
		}

		n, addr, retry, err := q.wait(p, timeout, changed)
		if timer != nil {
			timer.Stop()
		}
		if !retry {
			return n, addr, err
		}
		// Prazo alterado durante a espera: recomeça com o novo valor.
	}
}

func (q *packetQueue) wait(p []byte, timeout <-chan time.Time, changed <-chan struct{}) (n int, addr net.Addr, retry bool, err error) {
	select {
	case d := <-q.ch:
		return copy(p, d.data), d.addr, false, nil
	case <-q.closed:
		return 0, nil, false, q.closeErr()
	case <-timeout:
		return 0, nil, false, os.ErrDeadlineExceeded
	case <-changed:
		return 0, nil, true, nil
	}
}

func (q *packetQueue) setDeadline(t time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deadline = t
	close(q.deadlineCh)
	q.deadlineCh = make(chan struct{})
}

// close fecha a fila; leituras pendentes e futuras retornam err.
func (q *packetQueue) close(err error) {
	q.once.Do(func() {
		q.mu.Lock()
		q.err = err
		q.mu.Unlock()
		close(q.closed)
	})
}

func (q *packetQueue) closeErr() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

func (q *packetQueue) isClosed() bool {
	select {
	case <-q.closed:
		return true
	default:
		return false
	}
}

// MemAddr é o endereço de uma ponta de Pipe.
type MemAddr string

func (a MemAddr) Network() string { return "mem" }
func (a MemAddr) String() string  { return string(a) }

// memConn é uma ponta de Pipe.
type memConn struct {
	local MemAddr
	in    *packetQueue
	peer  *memConn
}

// Pipe cria duas pontas de uma conexão de datagramas em memória. Tudo que
// uma ponta escreve chega à outra, em ordem, sem perdas, exceto quando a fila
// de recepção (queueSize datagramas; <= 0 usa DefaultQueueSize) está cheia.
// O endereço de destino passado a WriteTo é ignorado.
func Pipe(queueSize int) (a, b PacketConn) {
	ca := &memConn{local: "mem-a", in: newPacketQueue(queueSize)}
	cb := &memConn{local: "mem-b", in: newPacketQueue(queueSize)}
	ca.peer, cb.peer = cb, ca
	return ca, cb
}

func (c *memConn) ReadFrom(p []byte) (int, net.Addr, error) { return c.in.read(p) }

func (c *memConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if c.in.isClosed() {
		return 0, net.ErrClosed
	}
	// Datagrama descartado silenciosamente se a outra ponta estiver cheia
	// ou fechada, como em UDP.
	c.peer.in.push(datagram{data: append([]byte(nil), p...), addr: c.local})
	return len(p), nil
}

func (c *memConn) SetReadDeadline(t time.Time) error {
	c.in.setDeadline(t)
	return nil
}

func (c *memConn) LocalAddr() net.Addr { return c.local }

func (c *memConn) Close() error {
	c.in.close(net.ErrClosed)
	return nil
}
