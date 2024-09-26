package main

import (
	"log"

	"github.com/saveole/proglog/internel/server"
)

func main() {
	srv := server.NewHTTPServer(":8080")
	log.Fatal(srv.ListenAndServe())
}