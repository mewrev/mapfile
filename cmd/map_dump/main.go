package main

import (
	"flag"
	"log"

	"github.com/kr/pretty"
	"github.com/mewrev/mapfile"
)

func main() {
	flag.Parse()
	for _, mapPath := range flag.Args() {
		m, err := mapfile.ParseFile(mapPath)
		if err != nil {
			log.Fatalf("%+v", err)
		}
		pretty.Println(m)
	}
}
