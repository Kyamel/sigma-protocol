package main

import (
	"flag"
	"fmt"
)

func main() {
	bind := flag.String("bind", "127.0.0.1:9000", "endereço UDP do servidor")
	dir := flag.String("dir", "./shared", "diretório de arquivos compartilhados")
	flag.Parse()

	fmt.Println("Sigma server")
	fmt.Printf("Endereço: %s\nDiretório: %s\n", *bind, *dir)
	fmt.Println("Protocolo ainda não implementado; nenhum socket foi aberto.")
}
