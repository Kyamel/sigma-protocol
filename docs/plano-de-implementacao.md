# Plano de implementação — Sigma Protocol

Base: `tp-rc2.pdf`, CSI510 — Redes de Computadores II. Entrega e apresentação
até **15/12/2026**. Este documento é uma proposta; o transporte ainda não foi
implementado e não há resultados experimentais.

## Requisitos e aceite

| Requisito                    | Implementação prevista                            | Evidência                                                    |
| ---------------------------- | ------------------------------------------------- | ------------------------------------------------------------ |
| Cliente/servidor UDP e senha | CLI, sessão autenticada, LIST/GET/PUT             | Listagem, download e upload; senha inválida rejeitada        |
| Confiabilidade e ordem       | Selective Repeat, ACK individual, buffer limitado | Arquivos idênticos sob perda, duplicação e reordenação       |
| Cabeçalho próprio            | Codec binário versionado                          | Especificação, vetores de codificação e captura anotada      |
| Temporização                 | RTO adaptativo, backoff, limite de tentativas     | Recuperação de DATA/ACK/controle perdidos ou erro delimitado |
| Integridade                  | CRC32C por datagrama e SHA-256 por arquivo        | Hash original e recebido no log e no relatório               |
| Quatro cenários              | Matriz de RTT/perda e arquivos ≥80 MB             | Dados brutos, parâmetros, gráficos e repetições              |
| Mini-CDN                     | Dois servidores em máquinas distintas             | RTTs, escolha automática e comparação com seleção manual     |
| Capturas                     | tshark/tcpdump e inspeção no Wireshark            | ACKs, retransmissões e seleção de servidor                   |
| Entrega                      | Código, README, relatório e apresentação          | Todos os integrantes reproduzem e explicam o sistema         |

O servidor aceitará uploads, operação explicitamente exigida para o cliente.
Não substituir o transporte solicitado por TCP, HTTP ou biblioteca QUIC.
O PDF exige relatório final SBC; `article/` usa ACM por solicitação do grupo.
Resolver essa diferença antes da entrega. Bônus entram depois do obrigatório.

## Arquitetura e dependências

A primeira versão usa apenas a biblioteca padrão Go: `net.UDPConn`,
`encoding/binary`, `hash/crc32` (Castagnoli), `crypto/sha256`, `crypto/rand`,
`crypto/hmac`, `io`, `os`, `time`, `context`, `flag`, `log/slog`,
`encoding/json`, `encoding/csv` e `testing`. O módulo
`github.com/PedroAlvesPaula/sigma-protocol` e os pontos de entrada do cliente e servidor
já foram criados, sem implementar o protocolo. Manter go.mod/go.sum se
dependências externas forem adicionadas.
Nix fornece compilador, análise, depuração, LaTeX, capturas e gráficos.

Estrutura proposta (`cmd/` já contém os esqueletos iniciais):

```text
cmd/sigma-client/      CLI: list, get, put, probe e seleção automática
cmd/sigma-server/      CLI: endereço, arquivos compartilhados e senha
internal/wire/        codec e validação de mensagens
internal/transport/   sessões, janela, ACKs, timers e retransmissões
internal/auth/        desafio/resposta, expiração e sessão
internal/files/       catálogo, arquivos temporários e hashes
internal/discovery/   sondagem e escolha entre endpoints
internal/metrics/     eventos JSONL e CSV por transferência
scripts/              matriz experimental e geração de gráficos
experiments/          parâmetros, dados brutos, hashes e capturas selecionadas
docs/                 especificação do protocolo e instruções experimentais
article/              relatório e figuras
```

Estado de cada sessão sob uma goroutine/event loop ou sincronização explícita.
Leitura/escrita incremental, sem carregar arquivos inteiros. Limitar sessões,
janela, payload, tamanho de arquivo, tempo ocioso e tentativas. Confinar caminhos
ao diretório do servidor, incluindo links simbólicos. Upload escreve temporário
e publica por rename somente após validar integridade.

## Protocolo proposto — versão 1

Criar `docs/protocolo.md` antes do codec. Inteiros unsigned em network byte order.
Datagrama de aplicação limitado a 1.200 bytes, incluindo cabeçalho de 44 bytes;
payload máximo de 1.156 bytes. Isso reduz fragmentação em redes usuais, mas não
garante adequação a caminhos de MTU menor, limitação a documentar.

| Offset | Bytes | Campo          | Semântica                                       |
| ------ | ----- | -------------- | ----------------------------------------------- |
| 0      | 4     | Magic          | Identificador fixo Sigma                        |
| 4      | 1     | Version        | Inicialmente 1                                  |
| 5      | 1     | Type           | Tipo de mensagem                                |
| 6      | 2     | Flags          | Reservado, zero na versão 1                     |
| 8      | 8     | Session ID     | ID aleatório da sessão                          |
| 16     | 8     | Transfer ID    | ID da operação; zero fora da transferência      |
| 24     | 4     | Sequence       | Índice de bloco ou ID de controle               |
| 28     | 4     | ACK            | Sequence confirmado, conforme Type              |
| 32     | 4     | Receive window | Capacidade em blocos a partir da base receptora |
| 36     | 2     | Payload length | Tamanho exato do payload                        |
| 38     | 2     | Header length  | 44 na versão 1                                  |
| 40     | 4     | CRC32C         | Cabeçalho com este campo zerado mais payload    |

Validar tamanhos, versão, CRC e associação à sessão antes de alterar estado.
Definir tamanho máximo de `(2^32 - 1) * 1156` bytes por transferência para evitar
wrap de Sequence. Arquivo vazio também deve passar por FIN e validação.

Tipos: HELLO, CHALLENGE, AUTH, AUTH_OK, LIST, LIST_RESULT, GET, PUT, META, DATA,
ACK, FIN, FIN_ACK, ERROR, PING e PONG. Controle/metadados podem usar JSON limitado
ao payload; LIST_RESULT exige paginação. META inclui nome, tamanho, tamanho do
bloco e SHA-256 original. Calcular hash recebido sobre o arquivo completo em ordem.

Autenticação simples: desafio aleatório de uso único, expiração e HMAC-SHA256 da
sessão e desafio com chave derivada da senha. Comparar com `hmac.Equal`.
Receber segredo por ambiente/entrada interativa, não argumentos; não registrar
credenciais. Isso não oferece confidencialidade nem autenticação forte do
servidor e permite ataque offline de dicionário com senha fraca. Criptografia é
uma possível extensão, com especificação própria.

Estados: descoberta → autenticação → sessão pronta → negociação → transferência
→ validação → conclusão. Controle tem IDs, timeout, repetição limitada e resposta
cacheada para idempotência. PUT repetido não deve truncar arquivo. FIN_ACK só é
enviado após validar hash; manter estado final temporariamente para responder
FIN repetido. Hash incorreto, falta de espaço e timeout geram erro explícito e
limpeza de temporários/estado.

## Janela, perdas e temporização

1. Selective Repeat com ACK individual. Manter base de envio, próximo Sequence,
   bitmap de ACKs e horário por pacote. Avançar a base apenas sobre ACKs contíguos,
   evitando ultrapassar a faixa da janela receptora.
2. Buffer limitado para blocos fora de ordem; escrever apenas prefixo contíguo.
   Duplicatas antigas recebem novo ACK sem reescrita; descartar fora da janela.
3. Usar a menor janela entre configuração local e capacidade anunciada. Prever
   sonda de janela zero para não travar após atualização perdida.
4. SRTT/RTTVAR com amostras não retransmitidas (regra de Karn). Proposta: RTO
   inicial de 1 s, depois `SRTT + max(G, 4*RTTVAR)`, piso experimental de 200 ms,
   teto de 5 s, backoff exponencial. Registrar parâmetros; inspiração na RFC 6298,
   sem alegar conformidade normativa com TCP.
5. Retransmitir apenas blocos vencidos; até 10 retransmissões por bloco, timeout
   de inatividade e cancelamento. Tratar perda de ACK, controle e encerramento.
6. Pacing e limite configurável de envio. Controle de fluxo não equivale a
   controle de congestionamento: a versão inicial não oferece equidade TCP;
   executar experimentos controlados e limitar tráfego fora do laboratório.

## Etapas e testes

| Etapa | Entrega                              | Critério de conclusão                                                                 |
| ----- | ------------------------------------ | ------------------------------------------------------------------------------------- |
| 1     | go.mod, CLIs, especificação e codec  | Round-trip binário, rejeição de entradas inválidas e go vet                           |
| 2     | Autenticação, LIST, GET/PUT e hashes | Transferências locais, senha inválida, arquivo vazio/inexistente e caminhos indevidos |
| 3     | Janela, timers, falhas e métricas    | Recuperação de DATA/ACK/controle perdidos, duplicação, reordenação e janela zero      |
| 4     | Mini-CDN                             | Escolha correta, empate e indisponibilidade em duas máquinas                          |
| 5     | Experimentos e capturas              | Arquivos ≥80 MB, hashes idênticos e dados para todos os cenários                      |
| 6     | Relatório e demonstração             | Reprodução independente e domínio por todos os integrantes                            |

Testes de transporte usam adaptador de datagramas com relógio controlável e
sementes fixas. Cobrir corrupção/truncamento, ACK atrasado, concorrência, FIN_ACK
perdido, desconexão, falta de espaço e divergência de hash. Executar
`go test ./...`, `go test -race ./...`, `go vet ./...`, `staticcheck ./...` e
fuzzing do decoder. Integração usa também UDP real, não só simulador; testes
automatizados não precisam alterar interfaces do desenvolvedor.

## Experimentos

| Cenário | RTT alvo | Perda por direção | Interpretação                        |
| ------- | -------- | ----------------- | ------------------------------------ |
| 1       | 25 ms    | 0%                | Servidor/CDN próximos                |
| 2       | 100 ms   | 0%                | Brasil–EUA/nuvem internacional       |
| 3       | 100 ms   | 5%                | Internacional com rede congestionada |
| 4       | 400 ms   | 5%                | GEO/enlace muito congestionado       |

Convenção: aplicar perda independentemente nos dois sentidos (DATA e ACK),
explicitando que 5% por direção não é 5% por troca completa. Usar `tc netem`
em interfaces isoladas Linux, com RTT/2 por direção e desconto da latência de
base; não aplicar o RTT inteiro em ambos os sentidos. Medir RTT efetivo e
documentar topologia e regras. Operar apenas interfaces de teste, com permissões
apropriadas e cleanup inclusive em interrupções. VMs/namespaces servem para
ensaios; a mini-CDN final exige máquinas diferentes.

Alternativa por aplicação: agendar cada datagrama independentemente; `sleep`
serial no loop introduz gargalo artificial. Captura no emissor pode observar
pacotes descartados depois pelo netem: correlacionar os dois extremos e logs.

- Arquivo de 80 MiB (83.886.080 bytes, acima de 80 MB), gerado uma vez, com SHA-256
  registrado. Não versionar arquivos grandes de teste.
- Janelas de 1, 8, 32, 128 e 512 blocos; janela 1 apenas como referência.
- Cinco repetições por cenário/janela/direção (download e upload), ordem aleatória
  e sementes registradas. Publicar também falhas e timeouts.
- Fixar/documentar limite de enlace, MTU, payload, buffers, versão Go, commit,
  CPU, SO, RTO, carga e política de perdas. Dimensionar filas netem, sobretudo
  em 400 ms, para não introduzir descartes não planejados.
- Separar tempo de sondagem/autenticação do tempo de transferência; conclusão
  percebida pelo usuário inclui a confirmação da integridade.

CSV: ID, commit, cenário, semente, direção, endpoint, RTT alvo/amostras, perda
configurada, janela, bytes úteis, bytes enviados de aplicação, datagramas
originais, retransmissões, duração, dois hashes e status. Eventos JSONL com tempo
monotônico relativo, sessão, transferência, tipo e Sequence explicam as capturas.

Goodput = bytes úteis confirmados / duração; throughput de aplicação = bytes de
datagramas enviados incluindo retransmissões / duração, em Mbit/s com conversão
explícita. Retransmissão é envio adicional do mesmo bloco; timeout é suspeita,
não medição direta de perda. Medir perda observada correlacionando IDs originais
dos dois extremos e separar perda de dados, ACKs e perda configurada. Apresentar
mediana/p95 do RTT, taxa de sucesso e média/dispersão entre repetições; documentar
o cálculo de intervalos de confiança, se usados.

Gráficos: goodput × janela por cenário; tempo de conclusão; retransmissões;
perda observada; RTT e ganho da escolha do servidor. Comparar tendências com
`janela * payload / RTT` como teto aproximado sem perdas, limitado pelo enlace,
sem confundir esse modelo com medição.

## Mini-CDN, capturas e entrega

Servidores em duas máquinas com cópias idênticas dos arquivos. Cliente recebe
endpoints configurados e envia cinco PINGs com nonce por servidor. Calcular RTT
no relógio local; escolher menor mediana entre os que retornarem pelo menos três
PONGs válidos. Empate pela ordem de configuração; ignorar indisponíveis e registrar
todos os RTTs e a decisão. Autenticar depois da escolha; senha inválida gera erro.

Comparar seleção automática com execução forçada em cada servidor, mantendo
arquivo/janela. Responder qual foi escolhido e impacto em goodput/tempo total.
Relacionar com mapeamento CDN, explicando que sondagem direta não implementa
DNS, anycast ou replicação automática, nem considera carga/capacidade.

Capturar seleção, autenticação com credenciais descartáveis, DATA/ACK e FIN.
Anotar Sequence repetido e intervalo de retransmissão com logs. Dissector Lua
é opcional; offsets e bytes UDP anotados são alternativa para o Wireshark.
Guardar parâmetros, CSV, hashes, gráficos e capturas representativas.

## Calendário e bônus

- 07–13/11: especificação, módulo Go, codec e CLIs.
- 14–20/11: autenticação, listagem, upload/download e hashes.
- 21–27/11: janela, retransmissões, testes e métricas.
- 28/11–04/12: mini-CDN e matriz experimental.
- 05–11/12: gráficos, relatório e revisão dos experimentos.
- 12–15/12: formato final, entrega e ensaio por todos os integrantes.

Depois do obrigatório: dashboard com `net/http`; multiplexação com isolamento por
Transfer ID e escalonamento documentado; IaaS com comparação local/nuvem.
Criptografia, migração ou 0-RTT exigem projeto específico; não alegar bônus sem
implementação e justificativa. Todos devem compreender o trabalho para a apresentação.

## Referências

- [Go: UDP](https://pkg.go.dev/net), [CRC32](https://pkg.go.dev/hash/crc32) e [SHA-256](https://pkg.go.dev/crypto/sha256).
- [RFC 9000 — QUIC](https://www.rfc-editor.org/rfc/rfc9000).
- [RFC 6298 — temporizadores](https://www.rfc-editor.org/rfc/rfc6298).
- [Classe oficial acmart](https://portalparts.acm.org/hippo/latex_templates/acmart.pdf).
