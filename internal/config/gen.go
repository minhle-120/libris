//go:build ignore

// Command gen writes the TypeScript config schema consumed by the frontend.
//
//	go generate ./internal/config
package main

import (
	"log"
	"os"

	"libris/internal/config"
)

const out = "../../web/src/lib/config.gen.ts"

func main() {
	f, err := os.Create(out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := config.Generate(f); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s", out)
}
