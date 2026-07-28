package main

import (
	"flag"
	"io"
	"log"
	"net"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18081", "TCP listen address")
	prefix := flag.String("prefix", "", "optional response prefix")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Printf("test TCP server listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serve(conn, []byte(*prefix))
	}
}

func serve(conn net.Conn, prefix []byte) {
	defer conn.Close()
	if len(prefix) > 0 {
		if _, err := conn.Write(prefix); err != nil {
			return
		}
	}
	_, _ = io.Copy(conn, conn)
}
