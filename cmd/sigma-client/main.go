package main

import (
	"flag"
	"fmt"
)

func main() {
	servers := flag.String("servers", "127.0.0.1:9000", "servidores UDP separados por vírgula")
	flag.Parse()

	fmt.Println("Sigma client")
	fmt.Printf("Servidores: %s\n", *servers)
	fmt.Println("Operações previstas: list, get, put e probe. Protocolo ainda não implementado.")
}
