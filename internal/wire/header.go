// Package wire implementa o formato binário dos datagramas do Sigma Protocol
// (versão 1), conforme docs/protocolo.md.
//
// Todo datagrama é formado por um cabeçalho fixo de 44 bytes seguido de um
// payload de 0 a 1.156 bytes. Inteiros são unsigned em network byte order
// (big-endian). O campo Checksum é um CRC32C (Castagnoli) calculado sobre o
// cabeçalho, com o próprio campo zerado, mais o payload.
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// Constantes do protocolo, versão 1.
const (
	// Magic identifica datagramas Sigma: ASCII "SGMA".
	Magic uint32 = 0x53474D41
	// Version é a versão do formato implementada por este pacote.
	Version uint8 = 1
	// HeaderLen é o tamanho fixo do cabeçalho na versão 1.
	HeaderLen = 44
	// MaxDatagram é o tamanho máximo de um datagrama de aplicação.
	MaxDatagram = 1200
	// MaxPayload é o tamanho máximo do payload de um datagrama.
	MaxPayload = MaxDatagram - HeaderLen
)

// Offsets dos campos dentro do cabeçalho.
const (
	offMagic      = 0
	offVersion    = 4
	offType       = 5
	offFlags      = 6
	offSessionID  = 8
	offTransferID = 16
	offSeq        = 24
	offAck        = 28
	offWindow     = 32
	offPayloadLen = 36
	offHeaderLen  = 38
	offChecksum   = 40
)

// Erros de validação. Use errors.Is para identificá-los; as mensagens
// retornadas trazem detalhes adicionais.
var (
	ErrShort           = errors.New("wire: datagrama menor que o cabeçalho")
	ErrBadMagic        = errors.New("wire: magic inválido")
	ErrVersion         = errors.New("wire: versão não suportada")
	ErrLength          = errors.New("wire: tamanho inconsistente")
	ErrChecksum        = errors.New("wire: checksum inválido")
	ErrFlags           = errors.New("wire: flags reservadas diferentes de zero")
	ErrType            = errors.New("wire: tipo de mensagem desconhecido")
	ErrPayloadTooLarge = errors.New("wire: payload excede o máximo")
	ErrShortBuffer     = errors.New("wire: buffer de destino insuficiente")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Header é a representação em memória do cabeçalho de 44 bytes.
type Header struct {
	Magic      uint32
	Version    uint8
	Type       MsgType
	Flags      uint16
	SessionID  uint64
	TransferID uint64
	Seq        uint32
	Ack        uint32
	Window     uint32
	PayloadLen uint16
	HeaderLen  uint16
	Checksum   uint32 // CRC32C (Castagnoli)
}

// Encode escreve em dst o datagrama formado por h e payload e retorna o
// número de bytes escritos.
//
// Os campos Magic, Version, PayloadLen, HeaderLen e Checksum de h são
// ignorados e preenchidos por Encode. O payload pode ocupar a mesma memória
// que dst[HeaderLen:], o que permite montar o payload no próprio buffer de
// envio sem cópia adicional.
func Encode(dst []byte, h Header, payload []byte) (int, error) {
	if len(payload) > MaxPayload {
		return 0, fmt.Errorf("%w: %d bytes (máximo %d)", ErrPayloadTooLarge, len(payload), MaxPayload)
	}
	if !h.Type.Valid() {
		return 0, fmt.Errorf("%w: %d", ErrType, uint8(h.Type))
	}
	if h.Flags != 0 {
		return 0, fmt.Errorf("%w: 0x%04x", ErrFlags, h.Flags)
	}
	n := HeaderLen + len(payload)
	if len(dst) < n {
		return 0, fmt.Errorf("%w: precisa de %d bytes, tem %d", ErrShortBuffer, n, len(dst))
	}

	b := dst[:n]
	// O payload é copiado primeiro: se ele estiver sobreposto a b, copy
	// trata a sobreposição corretamente e o cabeçalho não é afetado.
	copy(b[HeaderLen:], payload)

	be := binary.BigEndian
	be.PutUint32(b[offMagic:], Magic)
	b[offVersion] = Version
	b[offType] = byte(h.Type)
	be.PutUint16(b[offFlags:], h.Flags)
	be.PutUint64(b[offSessionID:], h.SessionID)
	be.PutUint64(b[offTransferID:], h.TransferID)
	be.PutUint32(b[offSeq:], h.Seq)
	be.PutUint32(b[offAck:], h.Ack)
	be.PutUint32(b[offWindow:], h.Window)
	be.PutUint16(b[offPayloadLen:], uint16(len(payload)))
	be.PutUint16(b[offHeaderLen:], HeaderLen)
	be.PutUint32(b[offChecksum:], 0)
	be.PutUint32(b[offChecksum:], crc32.Checksum(b, castagnoli))
	return n, nil
}

// Decode valida um datagrama recebido e retorna seu cabeçalho e payload.
//
// As validações seguem a ordem de docs/protocolo.md: tamanho mínimo, Magic,
// Version, Header length, Payload length, CRC32C, Flags e Type. A
// associação a sessão/transferência é responsabilidade da camada de
// transporte.
//
// O payload retornado é uma fatia de datagram, sem cópia: quem precisar
// guardá-lo depois que o buffer for reutilizado deve copiá-lo.
func Decode(datagram []byte) (Header, []byte, error) {
	var h Header
	if len(datagram) < HeaderLen {
		return h, nil, fmt.Errorf("%w: %d bytes", ErrShort, len(datagram))
	}

	be := binary.BigEndian
	h.Magic = be.Uint32(datagram[offMagic:])
	if h.Magic != Magic {
		return h, nil, fmt.Errorf("%w: 0x%08x", ErrBadMagic, h.Magic)
	}
	h.Version = datagram[offVersion]
	if h.Version != Version {
		return h, nil, fmt.Errorf("%w: %d", ErrVersion, h.Version)
	}
	h.HeaderLen = be.Uint16(datagram[offHeaderLen:])
	if h.HeaderLen != HeaderLen {
		return h, nil, fmt.Errorf("%w: header length %d (esperado %d)", ErrLength, h.HeaderLen, HeaderLen)
	}
	h.PayloadLen = be.Uint16(datagram[offPayloadLen:])
	if h.PayloadLen > MaxPayload {
		return h, nil, fmt.Errorf("%w: payload length %d (máximo %d)", ErrLength, h.PayloadLen, MaxPayload)
	}
	if got := len(datagram) - HeaderLen; int(h.PayloadLen) != got {
		return h, nil, fmt.Errorf("%w: payload length %d, mas o datagrama traz %d bytes", ErrLength, h.PayloadLen, got)
	}

	h.Checksum = be.Uint32(datagram[offChecksum:])
	var zero [4]byte
	sum := crc32.Update(0, castagnoli, datagram[:offChecksum])
	sum = crc32.Update(sum, castagnoli, zero[:])
	sum = crc32.Update(sum, castagnoli, datagram[HeaderLen:])
	if sum != h.Checksum {
		return h, nil, fmt.Errorf("%w: recebido 0x%08x, calculado 0x%08x", ErrChecksum, h.Checksum, sum)
	}

	h.Flags = be.Uint16(datagram[offFlags:])
	if h.Flags != 0 {
		return h, nil, fmt.Errorf("%w: 0x%04x", ErrFlags, h.Flags)
	}
	h.Type = MsgType(datagram[offType])
	if !h.Type.Valid() {
		return h, nil, fmt.Errorf("%w: %d", ErrType, uint8(h.Type))
	}

	h.SessionID = be.Uint64(datagram[offSessionID:])
	h.TransferID = be.Uint64(datagram[offTransferID:])
	h.Seq = be.Uint32(datagram[offSeq:])
	h.Ack = be.Uint32(datagram[offAck:])
	h.Window = be.Uint32(datagram[offWindow:])

	end := HeaderLen + int(h.PayloadLen)
	return h, datagram[HeaderLen:end:end], nil
}
