package transport

import (
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// Impairment descreve as falhas aplicadas aos datagramas em uma direção.
// Probabilidades vão de 0 a 1 e são sorteadas de forma independente para
// cada datagrama.
type Impairment struct {
	Loss      float64       // probabilidade de descartar o datagrama
	Duplicate float64       // probabilidade de entregar uma cópia extra
	Corrupt   float64       // probabilidade de alterar um byte de cada cópia
	Delay     time.Duration // atraso fixo de cada cópia
	Jitter    time.Duration // atraso extra uniforme em [0, Jitter); gera reordenação
}

func (im Impairment) validate(dir string) error {
	probs := []struct {
		name string
		p    float64
	}{{"Loss", im.Loss}, {"Duplicate", im.Duplicate}, {"Corrupt", im.Corrupt}}
	for _, pr := range probs {
		if math.IsNaN(pr.p) || pr.p < 0 || pr.p > 1 {
			return fmt.Errorf("transport: %s.%s = %v fora de [0, 1]", dir, pr.name, pr.p)
		}
	}
	if im.Delay < 0 || im.Jitter < 0 {
		return fmt.Errorf("transport: %s: Delay e Jitter não podem ser negativos", dir)
	}
	return nil
}

// LossyConfig configura um LossyConn.
type LossyConfig struct {
	// Seed torna os sorteios reprodutíveis. Cada direção usa um gerador
	// próprio derivado da semente, de modo que o tráfego de uma direção não
	// altera os sorteios da outra.
	Seed uint64
	// Send é aplicado aos datagramas escritos com WriteTo.
	Send Impairment
	// Recv é aplicado aos datagramas lidos da conexão interna.
	Recv Impairment
	// QueueSize é a capacidade da fila de recepção; <= 0 usa DefaultQueueSize.
	QueueSize int
}

// DirectionStats conta os eventos de uma direção.
type DirectionStats struct {
	Packets    uint64 // datagramas que entraram no simulador
	Dropped    uint64 // descartados por Loss
	Duplicated uint64 // que ganharam uma cópia extra
	Corrupted  uint64 // cópias com um byte alterado
	Overflow   uint64 // descartados por fila de recepção cheia (só Recv)
}

// LossyStats agrega as duas direções.
type LossyStats struct {
	Send, Recv DirectionStats
}

// LossyConn envolve um PacketConn e aplica perda, duplicação, corrupção,
// atraso e reordenação nos dois sentidos, com sorteios reprodutíveis.
//
// Cada datagrama é agendado de forma independente (time.AfterFunc); o laço
// de envio nunca dorme, para não introduzir gargalo artificial. Com atraso
// zero, a entrega é síncrona e a ordem é preservada.
//
// SetReadDeadline atua sobre a fila interna do LossyConn: a conexão interna
// é lida continuamente por uma goroutine própria até Close.
type LossyConn struct {
	inner PacketConn
	send  *impairer
	recv  *impairer
	queue *packetQueue

	closeOnce sync.Once
	closed    chan struct{}
	readDone  chan struct{}
}

var _ PacketConn = (*LossyConn)(nil)

// NewLossyConn cria um LossyConn sobre inner. A partir daqui inner pertence
// ao LossyConn e não deve ser lido diretamente.
func NewLossyConn(inner PacketConn, cfg LossyConfig) (*LossyConn, error) {
	if err := cfg.Send.validate("Send"); err != nil {
		return nil, err
	}
	if err := cfg.Recv.validate("Recv"); err != nil {
		return nil, err
	}
	c := &LossyConn{
		inner: inner,
		// Constantes distintas separam os fluxos aleatórios das direções.
		send:     newImpairer(cfg.Send, cfg.Seed, 0x53454e44), // "SEND"
		recv:     newImpairer(cfg.Recv, cfg.Seed, 0x52454356), // "RECV"
		queue:    newPacketQueue(cfg.QueueSize),
		closed:   make(chan struct{}),
		readDone: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *LossyConn) readLoop() {
	defer close(c.readDone)
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := c.inner.ReadFrom(buf)
		if err != nil {
			if c.isClosed() {
				c.queue.close(net.ErrClosed)
			} else {
				c.queue.close(err)
			}
			return
		}
		c.recv.apply(buf[:n], c.closed, func(b []byte) error {
			if !c.queue.push(datagram{data: b, addr: addr}) {
				c.recv.countOverflow()
			}
			return nil
		})
	}
}

// ReadFrom lê o próximo datagrama já submetido às falhas de recepção.
func (c *LossyConn) ReadFrom(p []byte) (int, net.Addr, error) {
	return c.queue.read(p)
}

// WriteTo submete p às falhas de envio. Como em UDP, um datagrama
// descartado ou atrasado não gera erro; erros só são devolvidos quando a
// entrega à conexão interna é síncrona.
func (c *LossyConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	err := c.send.apply(p, c.closed, func(b []byte) error {
		_, err := c.inner.WriteTo(b, addr)
		return err
	})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *LossyConn) SetReadDeadline(t time.Time) error {
	c.queue.setDeadline(t)
	return nil
}

func (c *LossyConn) LocalAddr() net.Addr { return c.inner.LocalAddr() }

// Close fecha a conexão interna e descarta entregas ainda agendadas.
func (c *LossyConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		c.queue.close(net.ErrClosed)
		err = c.inner.Close()
		<-c.readDone
	})
	return err
}

// Stats retorna um retrato dos contadores.
func (c *LossyConn) Stats() LossyStats {
	return LossyStats{Send: c.send.snapshot(), Recv: c.recv.snapshot()}
}

func (c *LossyConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// impairer aplica um Impairment com gerador próprio.
type impairer struct {
	cfg Impairment

	mu    sync.Mutex
	rng   *rand.Rand
	stats DirectionStats
}

func newImpairer(cfg Impairment, seed, stream uint64) *impairer {
	return &impairer{cfg: cfg, rng: rand.New(rand.NewPCG(seed, stream))}
}

type delivery struct {
	data  []byte
	delay time.Duration
}

// plan sorteia o destino de p: nenhuma, uma ou duas cópias, cada uma
// possivelmente corrompida e com seu próprio atraso.
func (im *impairer) plan(p []byte) []delivery {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.stats.Packets++
	if im.cfg.Loss > 0 && im.rng.Float64() < im.cfg.Loss {
		im.stats.Dropped++
		return nil
	}
	copies := 1
	if im.cfg.Duplicate > 0 && im.rng.Float64() < im.cfg.Duplicate {
		im.stats.Duplicated++
		copies = 2
	}
	out := make([]delivery, copies)
	for i := range out {
		b := append([]byte(nil), p...)
		if im.cfg.Corrupt > 0 && len(b) > 0 && im.rng.Float64() < im.cfg.Corrupt {
			im.stats.Corrupted++
			// XOR com valor não nulo: o byte sempre muda.
			b[im.rng.IntN(len(b))] ^= byte(1 + im.rng.IntN(255))
		}
		d := im.cfg.Delay
		if im.cfg.Jitter > 0 {
			d += time.Duration(im.rng.Int64N(int64(im.cfg.Jitter)))
		}
		out[i] = delivery{data: b, delay: d}
	}
	return out
}

// apply executa o plano para p. Entregas sem atraso acontecem na hora; as
// demais são agendadas e canceladas se closed for fechado antes.
func (im *impairer) apply(p []byte, closed <-chan struct{}, deliver func([]byte) error) error {
	var first error
	for _, d := range im.plan(p) {
		if d.delay == 0 {
			if err := deliver(d.data); err != nil && first == nil {
				first = err
			}
			continue
		}
		data := d.data
		time.AfterFunc(d.delay, func() {
			select {
			case <-closed:
			default:
				_ = deliver(data)
			}
		})
	}
	return first
}

func (im *impairer) countOverflow() {
	im.mu.Lock()
	im.stats.Overflow++
	im.mu.Unlock()
}

func (im *impairer) snapshot() DirectionStats {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.stats
}
