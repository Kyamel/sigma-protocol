package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"math/rand/v2"
	"strings"
	"testing"
)

// allTypes lista os tipos válidos em ordem determinística.
func allTypes() []MsgType {
	var ts []MsgType
	for t := 0; t <= 0xff; t++ {
		if MsgType(t).Valid() {
			ts = append(ts, MsgType(t))
		}
	}
	return ts
}

// normalized retorna h com os campos que Encode preenche ajustados, para
// comparação com o resultado de Decode.
func normalized(h Header, payload []byte, checksum uint32) Header {
	h.Magic = Magic
	h.Version = Version
	h.HeaderLen = HeaderLen
	h.PayloadLen = uint16(len(payload))
	h.Checksum = checksum
	return h
}

func mustEncode(t testing.TB, h Header, payload []byte) []byte {
	t.Helper()
	buf := make([]byte, MaxDatagram)
	n, err := Encode(buf, h, payload)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf[:n]
}

// rawDatagram monta um datagrama sem as validações de Encode, com CRC
// correto, para testar rejeições que ocorrem depois da verificação do CRC.
func rawDatagram(magic uint32, version uint8, typ uint8, flags uint16, payloadLen, headerLen uint16, payload []byte) []byte {
	b := make([]byte, HeaderLen+len(payload))
	be := binary.BigEndian
	be.PutUint32(b[offMagic:], magic)
	b[offVersion] = version
	b[offType] = typ
	be.PutUint16(b[offFlags:], flags)
	be.PutUint16(b[offPayloadLen:], payloadLen)
	be.PutUint16(b[offHeaderLen:], headerLen)
	copy(b[HeaderLen:], payload)
	be.PutUint32(b[offChecksum:], crc32.Checksum(b, castagnoli))
	return b
}

func TestLayoutConstants(t *testing.T) {
	if HeaderLen != 44 || MaxDatagram != 1200 || MaxPayload != 1156 {
		t.Fatalf("constantes divergem da especificação: header=%d datagram=%d payload=%d",
			HeaderLen, MaxDatagram, MaxPayload)
	}
	if offChecksum+4 != HeaderLen {
		t.Fatalf("checksum deve terminar no fim do cabeçalho")
	}
}

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, typ := range allTypes() {
		for _, size := range []int{0, 1, MaxPayload} {
			h := Header{
				Type:       typ,
				SessionID:  rng.Uint64(),
				TransferID: rng.Uint64(),
				Seq:        rng.Uint32(),
				Ack:        rng.Uint32(),
				Window:     rng.Uint32(),
			}
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(rng.IntN(256))
			}

			dg := mustEncode(t, h, payload)
			if len(dg) != HeaderLen+size {
				t.Fatalf("%v/%d: tamanho %d", typ, size, len(dg))
			}
			got, gotPayload, err := Decode(dg)
			if err != nil {
				t.Fatalf("%v/%d: Decode: %v", typ, size, err)
			}
			want := normalized(h, payload, got.Checksum)
			if got != want {
				t.Fatalf("%v/%d: cabeçalho\n got %+v\nwant %+v", typ, size, got, want)
			}
			if !bytes.Equal(gotPayload, payload) {
				t.Fatalf("%v/%d: payload diferente", typ, size)
			}
		}
	}
}

// TestGoldenVector fixa a codificação byte a byte. O mesmo vetor está em
// docs/protocolo.md e serve para conferir capturas no Wireshark.
func TestGoldenVector(t *testing.T) {
	h := Header{
		Type:       TypeData,
		SessionID:  0x0102030405060708,
		TransferID: 0x1112131415161718,
		Seq:        42,
		Ack:        0,
		Window:     32,
	}
	payload := []byte("sigma")
	const want = "5347" + "4d41" + // Magic "SGMA"
		"01" + // Version
		"22" + // Type DATA
		"0000" + // Flags
		"0102030405060708" + // Session ID
		"1112131415161718" + // Transfer ID
		"0000002a" + // Sequence 42
		"00000000" + // ACK
		"00000020" + // Window 32
		"0005" + // Payload length
		"002c" + // Header length 44
		"dcc6f1d7" + // CRC32C
		"7369676d61" // "sigma"

	got := hex.EncodeToString(mustEncode(t, h, payload))
	if got != want {
		t.Fatalf("vetor divergente\n got %s\nwant %s", got, want)
	}
}

func TestEveryBitFlipDetected(t *testing.T) {
	h := Header{Type: TypeData, SessionID: 7, TransferID: 9, Seq: 3, Ack: 2, Window: 64}
	dg := mustEncode(t, h, []byte("payload de teste"))
	for i := range len(dg) * 8 {
		c := bytes.Clone(dg)
		c[i/8] ^= 1 << (i % 8)
		if _, _, err := Decode(c); err == nil {
			t.Fatalf("inversão do bit %d (byte %d) não detectada", i, i/8)
		}
	}
}

func TestTruncatedRejected(t *testing.T) {
	dg := mustEncode(t, Header{Type: TypeData}, []byte("0123456789"))
	for n := range len(dg) {
		_, _, err := Decode(dg[:n])
		if err == nil {
			t.Fatalf("prefixo de %d bytes aceito", n)
		}
		if n < HeaderLen && !errors.Is(err, ErrShort) {
			t.Fatalf("prefixo de %d bytes: esperado ErrShort, obtido %v", n, err)
		}
		if n >= HeaderLen && !errors.Is(err, ErrLength) {
			t.Fatalf("prefixo de %d bytes: esperado ErrLength, obtido %v", n, err)
		}
	}
}

func TestDecodeRejections(t *testing.T) {
	valid := uint8(TypeData)
	big := make([]byte, MaxPayload+1)
	tests := []struct {
		name string
		dg   []byte
		want error
	}{
		{"vazio", nil, ErrShort},
		{"magic", rawDatagram(0xdeadbeef, Version, valid, 0, 0, HeaderLen, nil), ErrBadMagic},
		{"versão", rawDatagram(Magic, 2, valid, 0, 0, HeaderLen, nil), ErrVersion},
		{"header length", rawDatagram(Magic, Version, valid, 0, 0, 40, nil), ErrLength},
		{"payload length maior que o restante", rawDatagram(Magic, Version, valid, 0, 10, HeaderLen, []byte("abc")), ErrLength},
		{"bytes extras após o payload", rawDatagram(Magic, Version, valid, 0, 1, HeaderLen, []byte("abc")), ErrLength},
		{"payload acima do máximo", rawDatagram(Magic, Version, valid, 0, MaxPayload+1, HeaderLen, big), ErrLength},
		{"flags", rawDatagram(Magic, Version, valid, 1, 0, HeaderLen, nil), ErrFlags},
		{"tipo zero", rawDatagram(Magic, Version, 0, 0, 0, HeaderLen, nil), ErrType},
		{"tipo desconhecido", rawDatagram(Magic, Version, 0xff, 0, 0, HeaderLen, nil), ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Decode(tc.dg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("esperado %v, obtido %v", tc.want, err)
			}
		})
	}

	t.Run("checksum", func(t *testing.T) {
		dg := mustEncode(t, Header{Type: TypeAck, Ack: 5}, nil)
		dg[offAck+3] ^= 0x01
		if _, _, err := Decode(dg); !errors.Is(err, ErrChecksum) {
			t.Fatalf("esperado ErrChecksum, obtido %v", err)
		}
	})
}

func TestEncodeRejections(t *testing.T) {
	buf := make([]byte, MaxDatagram+1)
	if _, err := Encode(buf, Header{Type: TypeData}, make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("payload grande: %v", err)
	}
	if _, err := Encode(buf, Header{Type: 0}, nil); !errors.Is(err, ErrType) {
		t.Fatalf("tipo inválido: %v", err)
	}
	if _, err := Encode(buf, Header{Type: TypeData, Flags: 0x8000}, nil); !errors.Is(err, ErrFlags) {
		t.Fatalf("flags: %v", err)
	}
	if _, err := Encode(make([]byte, HeaderLen+2), Header{Type: TypeData}, []byte("abc")); !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("buffer curto: %v", err)
	}
}

// TestEncodeInPlace verifica o uso sem cópia: payload montado diretamente
// em dst[HeaderLen:].
func TestEncodeInPlace(t *testing.T) {
	buf := make([]byte, MaxDatagram)
	payload := buf[HeaderLen : HeaderLen+5]
	copy(payload, "hello")
	n, err := Encode(buf, Header{Type: TypeData, Seq: 1}, payload)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := Decode(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("payload %q", got)
	}
}

// TestDecodedPayloadCapacity garante que append no payload retornado não
// sobrescreve memória além do datagrama.
func TestDecodedPayloadCapacity(t *testing.T) {
	dg := mustEncode(t, Header{Type: TypeData}, []byte("abc"))
	_, p, err := Decode(dg)
	if err != nil {
		t.Fatal(err)
	}
	if cap(p) != len(p) {
		t.Fatalf("cap %d, len %d", cap(p), len(p))
	}
}

func TestMsgTypeString(t *testing.T) {
	if got := TypeFinAck.String(); got != "FIN_ACK" {
		t.Fatalf("FIN_ACK: %q", got)
	}
	if got := MsgType(0xff).String(); got != "UNKNOWN(0xff)" {
		t.Fatalf("desconhecido: %q", got)
	}
	seen := map[string]bool{}
	for _, typ := range allTypes() {
		s := typ.String()
		if strings.HasPrefix(s, "UNKNOWN") || seen[s] {
			t.Fatalf("nome inválido ou repetido: %v", s)
		}
		seen[s] = true
	}
	if len(seen) != 17 {
		t.Fatalf("esperados 17 tipos, encontrados %d", len(seen))
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add(mustEncode(f, Header{Type: TypeData, Seq: 1, Window: 8}, []byte("seed")))
	f.Add(mustEncode(f, Header{Type: TypeAck, Ack: 1}, nil))
	f.Add(rawDatagram(Magic, Version, uint8(TypeData), 0, 0xffff, HeaderLen, nil))

	f.Fuzz(func(t *testing.T, data []byte) {
		h, payload, err := Decode(data)
		if err != nil {
			return
		}
		// Todo datagrama aceito deve ser reproduzido exatamente pelo encoder.
		buf := make([]byte, MaxDatagram)
		n, err := Encode(buf, h, payload)
		if err != nil {
			t.Fatalf("datagrama aceito não pode ser recodificado: %v", err)
		}
		if !bytes.Equal(buf[:n], data) {
			t.Fatalf("recodificação diverge\n got %x\nwant %x", buf[:n], data)
		}
	})
}
