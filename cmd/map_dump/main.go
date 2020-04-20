package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/mewrev/mapfile"
)

func main() {
	flag.Parse()
	for _, mapPath := range flag.Args() {
		m, err := mapfile.ParseFile(mapPath)
		if err != nil {
			log.Fatalf("%+v", err)
		}
		dumpIdaScript(m)
	}
}

// dumpIdaScript converts the given symbol map file to a Python script for
// loading the symbols into IDA.
func dumpIdaScript(m *mapfile.Map) {
	for _, sym := range m.Syms {
		fmt.Printf("set_name(0x%08X, \"%s\", SN_NOWARN)\n", sym.Addr, sym.MangledName)
	}
}
