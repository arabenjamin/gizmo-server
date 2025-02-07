package main

import (
	"log"
	"os"

	"github.com/arabenjamin/gizmo-server/server"
)

func main() {

	serverlog := log.New(os.Stdout, "http: ", log.LstdFlags)
	log.Println("Starting Gizmo Server")

	serverlog.Println("Starting Gizmo Server")
	err := server.Start(serverlog)
	if err != nil {
		serverlog.Println("Critical error starting Gizmo Server")
		serverlog.Println(err)
	}

}
