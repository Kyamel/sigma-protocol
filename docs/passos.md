# Etapa 1 — Protocolo e confiabilidade: passo a passo

Base: `tp-rc2.pdf` (seção 4, "Protocolo de transporte") e
`docs/plano-de-implementacao.md`. Este documento detalha a primeira etapa de
implementação: cabeçalho próprio, janela deslizante, retransmissão por timeout
e entrega em ordem. Autenticação, LIST, mini-CDN e experimentos ficam para as
etapas seguintes, mas o que for feito aqui precisa servir a elas sem retrabalho.

## 1. Revisão da proposta inicial

| Proposta                                                          | Avaliação                                                                                                                                                                                                                                                                                                                           | Ajuste                                                                                                                                                                                                                                                         |
| ----------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Cabeçalho com seq, ACK, tipo, tamanho, checksum e janela          | Correto e cobre todos os exemplos do PDF. Faltam campos para validar que o datagrama é do protocolo (Magic/Version) e a qual sessão/transferência pertence, necessários em seguida para autenticação, upload e download simultâneos.                                                                                                | Usar o layout de 44 bytes do plano (Magic, Version, Type, Flags, Session ID, Transfer ID, Sequence, ACK, Window, Payload length, Header length, CRC32C). Validar tudo antes de alterar estado.                                                                  |
| Goroutines para enviar múltiplos pacotes simultaneamente          | **Problema conceitual.** Janela deslizante significa ter até N pacotes *em trânsito* sem esperar ACK, não enviá-los em paralelo. Uma goroutine por pacote não acelera nada (o gargalo é o RTT, não a CPU), embaralha a ordem de envio, cria condições de corrida sobre o estado da janela e é difícil de explicar na apresentação. | Um laço de eventos único por transferência (emissor) que envia enquanto houver espaço na janela, mais uma goroutine que só lê o socket e entrega ACKs por canal. Todo o estado da janela pertence a uma goroutine só.                                           |
| Timers individuais **ou** globais                                 | A escolha define o algoritmo. Timer global com retransmissão de tudo a partir da base é Go-Back-N; retransmitir "apenas os dados não confirmados" exige saber o vencimento de cada pacote, isto é, Selective Repeat.                                                                                                                | Selective Repeat: guardar o instante de envio e o prazo de cada pacote na janela; um único `time.Ticker` (ou `time.Timer` reprogramado para o prazo mais próximo) verifica vencimentos. Não criar um `time.Timer` por pacote. RTO adaptativo (seção 5.5).       |
| Buffer **no cliente** para reordenar antes de gravar              | O buffer está certo, mas o lugar não: no upload o servidor é o receptor. A lógica deve ser do **receptor**, independente de quem é cliente ou servidor.                                                                                                                                                                              | Componente `Receiver` reutilizado pelos dois lados, com buffer limitado ao tamanho da janela, ACK individual, reenvio de ACK para duplicatas e escrita apenas do prefixo contíguo.                                                                             |
| —                                                                 | Faltam itens que o PDF exige na mesma seção: detecção de dados corrompidos, validação por SHA-256 (com hash original e recebido), encerramento confiável e mensagens de erro. Também falta uma forma de **testar** perda sem depender de `tc netem`.                                                                                  | Acrescentar os passos 2 (simulador de falhas), 6 (FIN + SHA-256) e 7 (métricas). As métricas de retransmissão serão exigidas na seção 5 do PDF; é mais barato instrumentar agora.                                                                             |

Resumo: a proposta tem as quatro peças certas. As correções principais são
trocar "envio paralelo com goroutines" por "envio em rajada controlado por um
único laço" e definir explicitamente Selective Repeat com prazos por pacote.

## 2. Escopo

Dentro desta etapa:

- Especificação escrita do cabeçalho e das mensagens de transporte.
- Codec binário (`internal/wire`).
- Emissor e receptor Selective Repeat (`internal/transport`), com timeout,
  retransmissão seletiva, controle de fluxo pela janela anunciada e encerramento.
- Validação SHA-256 do arquivo completo.
- Simulador de perda, duplicação, reordenação, atraso e corrupção para testes.
- Contadores de métricas básicas.
- Uma demonstração mínima de transferência real por UDP (sem autenticação).

Fora desta etapa: autenticação, LIST paginado, múltiplas sessões concorrentes no
servidor, mini-CDN, matriz experimental com `tc netem`, gráficos e bônus.

## 3. Pacotes e arquivos

```text
docs/protocolo.md              especificação (passo 0)
internal/wire/header.go        struct Header, constantes, Encode/Decode
internal/wire/types.go         MsgType, códigos e nomes
internal/wire/header_test.go   round-trip, vetores fixos, entradas inválidas, fuzz
internal/transport/conn.go     interface PacketConn (UDP real ou simulado) e Pipe em memória
internal/transport/lossy.go    simulador de falhas com semente fixa
internal/transport/sender.go   emissor Selective Repeat
internal/transport/receiver.go receptor com buffer de reordenação
internal/transport/rto.go      estimador SRTT/RTTVAR/RTO
internal/transport/stats.go    contadores de métricas
internal/transport/*_test.go   testes unitários e de integração
```

## 4. Passos detalhados

### Passo 0 — Especificação (`docs/protocolo.md`)

Escrever antes do código. O PDF pede explicitamente que o cabeçalho seja
"definido e documentado".

1. Tabela de campos com offset, tamanho, tipo e semântica (copiar do plano e
   completar).
2. Para cada `Type` usado nesta etapa (DATA, ACK, FIN, FIN_ACK, ERROR, META),
   dizer quais campos são significativos e o que vai no payload.
3. Regras de validação na ordem em que são aplicadas (tamanho mínimo → Magic →
   Version → Header length → Payload length coerente com o datagrama → CRC32C →
   Session/Transfer ID conhecidos).
4. Semântica de Sequence: índice do bloco a partir de 0; bloco `i` corresponde
   aos bytes `[i*1156, min((i+1)*1156, tamanho))` do arquivo.
5. Semântica da janela anunciada: quantidade de blocos que o receptor ainda
   aceita a partir da sua base (`rcvBase`).
6. Diagrama de sequência de uma transferência com uma perda de DATA e uma perda
   de ACK.

**Critérios de aceitação**

- [ ] Documento revisado pelos três integrantes; cada um consegue explicar
      qualquer campo sem consultar o texto.
- [x] Todo campo do PDF (seq, ACK, tipo, tamanho, checksum, janela) aparece na
      tabela.
- [x] Byte order (big-endian) e tamanho máximo de datagrama (1.200 bytes)
      declarados.

### Passo 1 — Cabeçalho e codec (`internal/wire`)

```go
type Header struct {
    Magic         uint32
    Version       uint8
    Type          MsgType
    Flags         uint16
    SessionID     uint64
    TransferID    uint64
    Seq           uint32
    Ack           uint32
    Window        uint32
    PayloadLen    uint16
    HeaderLen     uint16
    Checksum      uint32 // CRC32C (Castagnoli)
}

func Encode(dst []byte, h Header, payload []byte) (int, error)
func Decode(datagram []byte) (Header, []byte, error)
```

1. Usar `binary.BigEndian.PutUint32/Uint32` etc. em offsets fixos. Evitar
   `binary.Write` com struct: é mais lento e depende do layout de memória.
2. `Encode` preenche `PayloadLen` e `HeaderLen`, escreve tudo com o campo de
   checksum zerado, calcula `crc32.Checksum(buf, castagnoliTable)` sobre
   cabeçalho + payload e grava o resultado nos bytes 40–43.
3. `Decode` aplica as validações do passo 0 e devolve erros distintos
   (`ErrShort`, `ErrBadMagic`, `ErrVersion`, `ErrLength`, `ErrChecksum`,
   `ErrType`). O payload retornado é uma fatia do buffer de entrada: documentar
   que quem guarda o payload precisa copiá-lo.
4. `MsgType` com `String()` para logs legíveis.
5. Nunca entrar em pânico com entrada arbitrária.

**Critérios de aceitação**

- [x] Round-trip: `Decode(Encode(h, p)) == (h, p)` para todos os tipos e para
      payloads de 0, 1 e 1.156 bytes.
- [x] Vetor fixo: um cabeçalho conhecido produz exatamente os bytes esperados
      (hex no teste e na especificação) — serve também para conferir no Wireshark.
- [x] Alterar qualquer bit do datagrama faz `Decode` retornar erro
      (teste percorrendo todos os bits de um datagrama pequeno).
- [x] Datagramas truncados, com `PayloadLen` maior que o restante ou com payload
      acima de 1.156 bytes são rejeitados.
- [x] `go test -fuzz=FuzzDecode` por pelo menos 1 minuto sem pânico.
- [x] `go vet ./...` e `staticcheck ./...` sem avisos.

### Passo 2 — Abstração de rede e simulador de falhas

Sem isso, não há como provar que a retransmissão funciona antes de configurar
`netem`, e os testes ficam lentos e não determinísticos.

```go
type PacketConn interface {
    ReadFrom(p []byte) (n int, addr net.Addr, err error)
    WriteTo(p []byte, addr net.Addr) (int, error)
    SetReadDeadline(t time.Time) error
    Close() error
}
```

1. `*net.UDPConn` já satisfaz a interface; a produção usa UDP real.
2. `LossyConn` envolve outro `PacketConn` e, com `math/rand` semeado, aplica por
   direção: probabilidade de descarte, duplicação, corrupção (inverte um byte
   aleatório), atraso fixo + jitter (gera reordenação). Cada datagrama é
   agendado de forma independente (`time.AfterFunc`), nunca com `sleep` no laço
   de envio — o `sleep` serial cria um gargalo artificial (ver plano).
3. Um par de `LossyConn` ligados em memória (canal) permite testes sem socket.

**Critérios de aceitação**

- [x] Com perda configurada em 5% e 100.000 datagramas, a perda medida fica
      entre 4,5% e 5,5%.
- [x] Mesma semente → mesma sequência de eventos (teste repetível).
- [x] Datagrama corrompido pelo simulador é rejeitado por `wire.Decode`.

### Passo 3 — Receptor com buffer de reordenação

Estado (uma goroutine é dona dele):

```text
rcvBase      próximo bloco que falta para escrever em ordem
buffer       map[uint32][]byte ou slice circular de tamanho W
window       W, capacidade máxima em blocos
```

Ao receber DATA válido com `seq`:

1. `seq < rcvBase` → duplicata antiga: **reenviar ACK** (o ACK anterior pode ter
   se perdido) e descartar o dado.
2. `rcvBase <= seq < rcvBase+W` → se ainda não estiver no buffer, **copiar** o
   payload e guardar; enviar ACK de `seq`.
3. `seq >= rcvBase+W` → fora da janela: descartar sem ACK (o emissor violou a
   janela ou é lixo).
4. Enquanto `buffer[rcvBase]` existir: escrever no arquivo (e no `sha256.Hash`),
   remover do buffer, `rcvBase++`.
5. Cada ACK carrega `Ack = seq` e `Window = W - len(buffer)` (espaço livre).

Detalhes:

- Escrever no arquivo de forma incremental (`bufio.Writer` sobre arquivo
  temporário); nunca manter o arquivo inteiro em memória.
- Datagrama com erro de CRC é descartado e contado; sem ACK (o timeout do
  emissor resolve).

**Critérios de aceitação**

- [ ] Entregando os blocos em ordem aleatória (permutação com semente), o
      arquivo escrito é idêntico ao original.
- [ ] Cada bloco duplicado gera novo ACK e não é escrito duas vezes.
- [ ] O buffer nunca passa de `W` entradas (verificado no teste).
- [ ] Bloco fora da janela é descartado e contado em métrica própria.

### Passo 4 — Emissor com janela deslizante

Estado (uma goroutine é dona dele):

```text
sendBase     menor bloco ainda não confirmado
nextSeq      próximo bloco nunca enviado
acked        bitmap/slice de bool para [sendBase, sendBase+W)
sentAt       instante do último envio de cada bloco na janela
deadline     prazo de retransmissão de cada bloco
retries      contagem de retransmissões por bloco
peerWindow   última janela anunciada pelo receptor
```

Laço de eventos:

```go
for {
    // 1. enviar enquanto houver espaço
    for nextSeq < total && nextSeq < sendBase+effectiveWindow() {
        send(nextSeq); nextSeq++
    }
    select {
    case ack := <-acks:      // vindo da goroutine leitora do socket
        onAck(ack)            // marca, amostra RTT, desliza sendBase
    case now := <-ticker.C:
        onTick(now)           // retransmite os blocos vencidos
    case <-ctx.Done():
        return ctx.Err()
    }
    if sendBase == total { break }
}
```

1. `effectiveWindow() = min(janelaLocal, peerWindow)`; com `peerWindow == 0`,
   enviar periodicamente um único bloco como sonda para não travar caso o ACK
   que reabre a janela se perca.
2. `onAck`: ignorar ACK fora de `[sendBase, nextSeq)`; marcar `acked`; deslizar
   `sendBase` enquanto o bloco da base estiver confirmado.
3. A goroutine leitora faz `ReadFrom` em loop, chama `wire.Decode`, descarta
   erros (contando-os) e envia uma **cópia** do cabeçalho pelo canal. Não passar
   a fatia do buffer de leitura, que será sobrescrita.
4. Ler os blocos do arquivo sob demanda (`ReadAt`) para não carregar 80 MB.
5. A janela local é parâmetro de linha de comando (`-window`), pois os
   experimentos vão variar 1, 8, 32, 128 e 512.

**Critérios de aceitação**

- [ ] Nunca há mais que `min(W, peerWindow)` blocos não confirmados (assert no
      teste).
- [ ] Com `W = 1` o comportamento é Stop-and-Wait (útil como linha de base no
      relatório).
- [ ] Em simulador sem perda com RTT de 25 ms, `W = 32` termina pelo menos 10×
      mais rápido que `W = 1` para um arquivo de 5 MB.
- [ ] `go test -race ./internal/transport/...` sem condições de corrida.

### Passo 5 — Timeout e retransmissão seletiva

1. `rto.go`: estimador inspirado na RFC 6298.
   - Primeira amostra: `SRTT = R`, `RTTVAR = R/2`.
   - Seguintes: `RTTVAR = 3/4·RTTVAR + 1/4·|SRTT − R|`,
     `SRTT = 7/8·SRTT + 1/8·R`.
   - `RTO = SRTT + max(G, 4·RTTVAR)`, limitado a [200 ms, 5 s]; RTO inicial 1 s.
2. Regra de Karn: só usar amostras de blocos **nunca retransmitidos**.
3. `onTick`: para cada bloco não confirmado com `deadline <= now`, reenviar,
   `retries++`, `deadline = now + RTO` e dobrar o RTO (backoff exponencial); o
   RTO volta ao valor calculado na próxima amostra válida.
4. Ticker de 10 ms (bem abaixo do RTO mínimo). Prazos com relógio monotônico
   (`time.Now()` em Go já carrega leitura monotônica).
5. Limites: bloco com mais de 10 retransmissões ou nenhum ACK válido por 30 s
   encerra a transferência com erro explícito ("servidor não responde após N
   tentativas"), envio de ERROR ao par e limpeza do temporário.

**Critérios de aceitação**

- [ ] Perda de DATA: o bloco perdido é reenviado **sozinho** (os demais blocos
      da janela não são reenviados) — verificado contando envios por `seq`.
- [ ] Perda de ACK: emissor reenvia, receptor responde com ACK de duplicata, a
      transferência continua.
- [ ] Teste do RTO com sequência fixa de amostras confere SRTT/RTTVAR/RTO
      esperados; amostras de blocos retransmitidos são ignoradas.
- [ ] Desligar o receptor no meio da transferência produz erro claro em tempo
      limitado, sem travar e sem pânico.

### Passo 6 — Encerramento e integridade (SHA-256)

O PDF exige validar o arquivo recebido por hash e apresentar o hash original e
o recebido.

1. Antes dos dados, o emissor envia META (nome, tamanho, tamanho de bloco,
   SHA-256 original), confirmado pelo receptor. Nesta etapa pode ser o único
   controle: GET/PUT completos ficam para a etapa 2.
2. Após o último ACK, o emissor envia FIN com retransmissão por timeout.
3. O receptor, ao ter escrito todos os blocos, finaliza o hash e compara com o
   SHA-256 de META:
   - igual → `rename` do temporário para o nome final e FIN_ACK;
   - diferente → apaga o temporário, envia ERROR ("hash divergente") e registra
     os dois valores.
4. O receptor mantém o estado final por alguns segundos para responder a FIN
   repetido (caso o FIN_ACK se perca).
5. Ambos os lados imprimem/registram o hash original e o recebido.
6. Arquivo vazio: 0 blocos, segue direto para FIN e validação do hash do vazio.

**Critérios de aceitação**

- [ ] Hash original e hash recebido aparecem no log dos dois lados.
- [ ] Arquivo vazio, arquivo de 1 byte, arquivo de exatamente 1.156 bytes e de
      1.157 bytes transferidos corretamente.
- [ ] Forçando um hash errado em META, o receptor recusa e não deixa arquivo
      final no disco.
- [ ] Perda do FIN_ACK não faz o emissor reportar falha.

### Passo 7 — Métricas mínimas

A seção 5 do PDF pede RTT, throughput, taxa de perda e número de retransmissões.

1. `Stats` por transferência: datagramas originais, retransmissões, ACKs
   recebidos, duplicatas recebidas, descartes por CRC, descartes fora da janela,
   bytes úteis, bytes enviados (com retransmissões), amostras de RTT, duração.
2. Ao terminar, imprimir resumo e gravar uma linha CSV (formato do plano).
3. Log opcional por evento (JSONL) com `seq`, tipo e tempo relativo, para
   correlacionar com capturas do Wireshark depois.

**Critérios de aceitação**

- [ ] Em simulação sem perda, retransmissões = 0.
- [ ] Em simulação com perda, o número de retransmissões é coerente com a perda
      configurada (ordem de grandeza) e aparece no resumo.
- [ ] Goodput e throughput calculados conforme definição do plano, em Mbit/s.

### Passo 8 — Integração com UDP real

1. Subcomando provisório nas CLIs (ex.: `sigma-server -bind :9000 -dir ./shared`
   e `sigma-client send/recv`) que usa o transporte sobre `net.UDPConn`, sem
   autenticação.
2. Transferir em loopback um arquivo de 80 MiB
   (`head -c 83886080 /dev/urandom > teste.bin`, fora do Git).
3. Capturar com `tshark -i lo -f "udp port 9000"` e conferir no Wireshark um
   DATA, um ACK e o vetor de bytes do passo 1.
4. Atualizar o README com os comandos.

**Critérios de aceitação**

- [ ] 80 MiB transferidos com hashes idênticos em loopback.
- [ ] Mesmo resultado com `LossyConn` envolvendo o socket real (5% de perda por
      direção, duplicação e reordenação).
- [ ] Captura mostra campos do cabeçalho nos offsets documentados.

## 5. Critérios de aceitação da etapa (mapeados ao PDF)

| Exigência do PDF (seção 4 / 3)              | Evidência para considerar a etapa concluída                                              |
| ------------------------------------------- | ---------------------------------------------------------------------------------------- |
| Cabeçalho próprio definido e documentado    | `docs/protocolo.md` + vetor de bytes testado + captura conferida                         |
| Entrega confiável                           | 20 sementes com 5% de perda, 1% duplicação, 1% corrupção e reordenação: todos os hashes iguais |
| Entrega em ordem                            | Teste de permutação do receptor + hash igual sob reordenação                             |
| Controle de retransmissões                  | Contador por `seq` mostra retransmissão apenas do bloco perdido; limite de tentativas    |
| Detecção de perdas                          | Timeout por bloco com RTO adaptativo e teste do estimador                                |
| Controle por temporização (timeout)         | Ticker + prazos por bloco; erro claro quando o par some                                  |
| Janela deslizante (evita perda de nota)     | `W > 1` comprovadamente mais rápido que `W = 1`; janela anunciada respeitada             |
| Integridade SHA-256                         | Hash original e recebido registrados; divergência gera erro e limpeza                    |
| Detectar dados corrompidos / erros (seção 3)| CRC32C rejeita datagramas alterados; mensagens de erro distintas e legíveis              |
| Qualidade                                   | `go test ./...`, `go test -race ./...`, `go vet ./...`, `staticcheck ./...` passando     |

## 6. Ordem e divisão sugeridas

1. Passo 0 em conjunto (todos precisam dominar o cabeçalho).
2. Em paralelo: passo 1 (codec) | passo 2 (simulador) | passo 5.1–5.2 (estimador
   de RTO, que é puro e testável isoladamente).
3. Passo 3 (receptor) e passo 4 (emissor) por pessoas diferentes, integrando
   sobre o simulador.
4. Passos 5.3–5.5, 6, 7 e 8 em sequência.

Ao fim, cada integrante explica o código que **não** escreveu (o FAQ do PDF
avisa que perguntas vão para quem demonstra menos domínio).

## 7. Perguntas de verificação para o grupo

- Por que Selective Repeat e não Go-Back-N? O que muda na retransmissão?
- O que acontece se um ACK se perde? E se o FIN_ACK se perde?
- Por que a regra de Karn é necessária?
- Qual o teto teórico de vazão com `W = 32`, payload 1.156 B e RTT 100 ms?
  (≈ 32 · 1156 · 8 / 0,1 ≈ 2,96 Mbit/s)
- Por que CRC32C por datagrama **e** SHA-256 por arquivo? O que cada um detecta?
- Por que enviar com goroutines paralelas não aumentaria a vazão?
