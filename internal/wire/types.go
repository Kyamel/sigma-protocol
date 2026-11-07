package wire

import "fmt"

// MsgType identifica o tipo de mensagem (campo Type, offset 5).
//
// Os códigos são agrupados por função para facilitar a leitura de capturas:
// 0x0_ autenticação, 0x1_ operações, 0x2_ transferência, 0x3_ diversos.
// O valor zero é inválido.
type MsgType uint8

const (
	TypeHello     MsgType = 0x01
	TypeChallenge MsgType = 0x02
	TypeAuth      MsgType = 0x03
	TypeAuthOK    MsgType = 0x04

	TypeList       MsgType = 0x10
	TypeListResult MsgType = 0x11
	TypeGet        MsgType = 0x12
	TypePut        MsgType = 0x13

	TypeMeta    MsgType = 0x20
	TypeMetaAck MsgType = 0x21
	TypeData    MsgType = 0x22
	TypeAck     MsgType = 0x23
	TypeFin     MsgType = 0x24
	TypeFinAck  MsgType = 0x25

	TypeError MsgType = 0x30
	TypePing  MsgType = 0x31
	TypePong  MsgType = 0x32
)

var typeNames = map[MsgType]string{
	TypeHello:      "HELLO",
	TypeChallenge:  "CHALLENGE",
	TypeAuth:       "AUTH",
	TypeAuthOK:     "AUTH_OK",
	TypeList:       "LIST",
	TypeListResult: "LIST_RESULT",
	TypeGet:        "GET",
	TypePut:        "PUT",
	TypeMeta:       "META",
	TypeMetaAck:    "META_ACK",
	TypeData:       "DATA",
	TypeAck:        "ACK",
	TypeFin:        "FIN",
	TypeFinAck:     "FIN_ACK",
	TypeError:      "ERROR",
	TypePing:       "PING",
	TypePong:       "PONG",
}

// Valid informa se t é um tipo definido na versão 1.
func (t MsgType) Valid() bool {
	_, ok := typeNames[t]
	return ok
}

// String retorna o nome do tipo, ou "UNKNOWN(0xNN)" se não estiver definido.
func (t MsgType) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN(0x%02x)", uint8(t))
}
