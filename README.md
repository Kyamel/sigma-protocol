# Sigma Protocol

Trabalho de Redes de Computadores II: transferência confiável de arquivos sobre UDP.

- [Enunciado](tp-rc2.pdf)
- [Plano de implementação](docs/plano-de-implementacao.md)
- [Relatório inicial ACM](article/main.tex)

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
```

## Relatório

```sh
make -C article
# PDF: article/main.pdf
```

O relatório inicial usa ACM conforme solicitado; o enunciado pede SBC para a entrega final.
