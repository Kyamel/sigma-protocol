# Sigma Protocol

Trabalho de Redes de Computadores II: transferência confiável de arquivos sobre UDP.

- [Enunciado](tp-rc2.pdf)
- [Plano de implementação](docs/plano-de-implementacao.md)
- [Passo a passo da etapa 1](docs/passos.md)
- [Especificação do protocolo](docs/protocolo.md)
- [Modelo SBC](article/main.tex)

## Ambiente

Go, LaTeX e ferramentas de experimentação:

```sh
nix develop path:.
# Ou, sem flakes:
nix-shell
```

## Executar

```sh
go run ./cmd/sigma-server
go run ./cmd/sigma-client
```

Os comandos são esqueletos iniciais, sem comunicação de rede. Use `-h` para ver as opções.

```sh
go build ./...
go test ./...
go vet ./...
staticcheck ./...
go test -run='^$' -fuzz=FuzzDecode -fuzztime=60s ./internal/wire/
```

`go test -race` precisa de cgo. Se o caminho do projeto tiver espaços, o
`nix develop` gera um `-L` quebrado em `NIX_LDFLAGS`; remova-o antes:

```sh
export NIX_LDFLAGS=$(echo "$NIX_LDFLAGS" | tr ' ' '\n' | grep -v outputs/out/lib | tr '\n' ' ')
go test -race ./...
```

## Relatório

```sh
make -C article
# PDF: article/main.pdf
```

Modelo SBC vazio, com arquivos de estilo obtidos do [modelo disponibilizado pelo SBGames](https://www.sbgames.org/arquivos/Template_SBC/template-latex/).
