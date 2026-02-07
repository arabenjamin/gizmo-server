package main

import (
	"log"
	"os"

	"github.com/arabenjamin/gizmo-server/server"
)

func main() {

	serverlog := log.New(os.Stdout, "http: ", log.LstdFlags)
	log.Println("Starting Gizmo Server")

	robotURL := os.Getenv("GIZMATRON_URL")
	if robotURL == "" {
		robotURL = "http://localhost:8080"
	}

	serverlog.Printf("Starting Gizmo Server (robot URL: %s)", robotURL)
	err := server.Start(serverlog, robotURL)
	if err != nil {
		serverlog.Println("Critical error starting Gizmo Server")
		serverlog.Println(err)
	}

}
