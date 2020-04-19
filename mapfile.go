// Package mapfile provides access to symbol map files (MAP file format)
// produced by linkers.
package mapfile

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Note: this package supports the MAP file format produced by Visual Studio
// Code. Future versions of mapfile may include support for the MAP file format
// produced by GCC.

// Map is a symbol map file.
type Map struct {
	// Name of linker output.
	Name string
	// Link date.
	Date time.Time
	// Base address (preferred load address).
	BaseAddr uint64
	// Segment relative offset to entry point.
	Entry SegmentOffset
	// Sections.
	Sects []*Section
	// Symbols.
	Syms []*Symbol
}

// ParseString parses the given symbol map file, reading from s.
func ParseString(s string) (*Map, error) {
	r := strings.NewReader(s)
	return Parse(r)
}

// ParseBytes parses the given symbol map file, reading from buf.
func ParseBytes(buf []byte) (*Map, error) {
	r := bytes.NewReader(buf)
	return Parse(r)
}

// ParseFile parses the given symbol map file, reading from mapPath.
func ParseFile(mapPath string) (*Map, error) {
	buf, err := ioutil.ReadFile(mapPath)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return ParseBytes(buf)
}

// Parse parses the given symbol map file, reading from r.
func Parse(r io.Reader) (*Map, error) {
	// Example contents of foo.MAP file:
	//
	//    FOO
	//
	//    Timestamp is 5e97f112 (Wed Apr 15 22:45:54 2020)
	//
	//    Preferred load address is 00400000
	//
	//    Start         Length     Name                   Class
	//    0001:00000000 001012c6H .text                   CODE
	//    0002:00000000 00007c18H .rdata                  DATA
	//    ...
	//
	//     Address         Publics by Value              Rva+Base   Lib:Object
	//
	//    0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
	//    0002:00000058       ?qux@@3PBDB                00503058   baz.obj
	//    0003:00000040       ?fob@@3PADA                0050b040   baz.obj
	//    0004:00000000       __IMPORT_DESCRIPTOR_KERNEL32 00731000   kernel32:KERNEL32.dll
	//    ...
	//
	//    entry point at        0001:000f0290
	//
	//    Static symbols
	//
	//    0001:000dc1c2       ?quux@@YIXXZ        004dd1c2 f quuz.obj
	//
	//    FIXUPS: 101506 21 13 21 15 21 39f 5f 114 211 10 17 9 4a 32 61 64 33 30
	//    ...
	m := &Map{}
	s := bufio.NewScanner(r)
	var (
		hasName         bool
		inSectList      bool
		inSymList       bool
		inStaticSymList bool
	)
loop:
	for s.Scan() {
		line := s.Text()
		line = strings.TrimSpace(line)
		switch {
		// Name of linker output.
		case !hasName:
			m.Name = line
			hasName = true // first line is linker output name.
			continue
		// Link date.
		case strings.HasPrefix(line, "Timestamp is "):
			// Timestamp is 5e97f112 (Wed Apr 15 22:45:54 2020)
			rawDate := line[len("Timestamp is 5e97f112 (") : len(line)-len(")")]
			date, err := time.Parse(time.ANSIC, rawDate)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.Date = date
		// Base address.
		case strings.HasPrefix(line, "Preferred load address is "):
			rawBaseAddr := line[len("Preferred load address is "):]
			baseAddr, err := strconv.ParseUint(rawBaseAddr, 16, 64)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.BaseAddr = baseAddr
		// List of sections (start).
		case strings.HasPrefix(line, "Start         Length     Name                   Class"):
			inSectList = true
		// List of sections.
		case inSectList:
			// Example:
			//
			//    0001:00000000 001012c6H .text                   CODE
			sect, ok, err := parseSection(line)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if !ok {
				inSectList = false
				continue
			}
			m.Sects = append(m.Sects, sect)
		// List of symbols (start).
		case strings.HasPrefix(line, "Address         Publics by Value              Rva+Base   Lib:Object"):
			inSymList = true
			// Skip empty line between header and list of symbols.
			if !s.Scan() {
				break loop
			}
			if line := s.Text(); len(line) != 0 {
				return nil, errors.Errorf("unexpected line between header and list of symbols; expected empty line, got %q", line)
			}
		// List of symbols.
		case inSymList:
			// Example:
			//
			//    0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
			sym, ok, err := parseSymbol(line)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if !ok {
				inSymList = false
				continue
			}
			m.Syms = append(m.Syms, sym)
		// Entry point.
		case strings.HasPrefix(line, "entry point at"):
			// Example:
			//
			//    entry point at        0001:000f0290
			rawEntry := strings.TrimSpace(strings.TrimPrefix(line, "entry point at"))
			entry, err := parseSegmentOffset(rawEntry)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.Entry = entry
		// List of static symbols (start).
		case strings.HasPrefix(line, "Static symbols"):
			inStaticSymList = true
			// Skip empty line between header and list of symbols.
			if !s.Scan() {
				break loop
			}
			if line := s.Text(); len(line) != 0 {
				return nil, errors.Errorf("unexpected line between header and list of static symbols; expected empty line, got %q", line)
			}
		// List of static symbols.
		case inStaticSymList:
			// Example:
			//
			//    0001:000dc1c2       ?quux@@YIXXZ        004dd1c2 f quuz.obj
			sym, ok, err := parseSymbol(line)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			if !ok {
				inStaticSymList = false
				continue
			}
			sym.IsStatic = true
			m.Syms = append(m.Syms, sym)
		case strings.HasPrefix(line, "FIXUPS:"):
			// ignore.
		}
	}
	if err := s.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	return m, nil
}

// Section tracks section linkage information.
//
// Example:
//    0001:00000000 001012c6H .text                   CODE
type Section struct {
	// Section name.
	Name string
	// Segment relative offset to start of section.
	Start SegmentOffset
	// Size of section in bytes.
	Size int
	// Section type (code or data).
	Type SectionType
}

// parseSection parses the string representation of the given section.
func parseSection(s string) (*Section, bool, error) {
	// Example:
	//
	//    0001:00000000 001012c6H .text                   CODE
	fields := strings.Fields(s)
	if len(fields) == 0 {
		// End of section list reached.
		return nil, false, nil
	}
	sect := &Section{}
	// Start of section (offset relative to segment).
	//
	//    0001:00000000
	rawStart := fields[0]
	start, err := parseSegmentOffset(rawStart)
	if err != nil {
		return nil, false, errors.WithStack(err)
	}
	sect.Start = start
	// Size in bytes.
	//
	//    001012c6H
	rawSize := strings.TrimSuffix(fields[1], "H")
	size, err := strconv.ParseUint(rawSize, 16, 64)
	if err != nil {
		return nil, false, errors.WithStack(err)
	}
	sect.Size = int(size)
	// Section name.
	//
	//    .text
	sect.Name = fields[2]
	// Section type.
	//
	//    CODE
	sect.Type = SectionTypeFromString(fields[3])
	return sect, true, nil
}

//go:generate stringer -linecomment -type SectionType
//go:generate string2enum -samepkg -linecomment -type SectionType

// SectionType specifies the type of a section (code or data).
type SectionType uint8

// Section types.
const (
	SectionTypeCode SectionType = iota + 1 // CODE
	SectionTypeData                        // DATA
)

// Symbol is a symbol with linker information.
//
// Example:
//    0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
type Symbol struct {
	// Demangled name of symbol.
	Name string
	// Mangled name of symbol (e.g. "_WinMain@16").
	MangledName string
	// Virtual address of symbol (relative virtual address + base address).
	Addr uint64
	// Segment relative offset to start of symbol.
	Start SegmentOffset
	// File name of object containing symbol ([libname:]filename).
	ObjectName string
	// Symbols is a function.
	IsFunc bool
	// Symbols is static.
	IsStatic bool
}

// parseSymbol parses the string representation of the given symbol.
func parseSymbol(s string) (*Symbol, bool, error) {
	// Example:
	//
	//    0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
	fields := strings.Fields(s)
	if len(fields) == 0 {
		// End of symbols list reached.
		return nil, false, nil
	}
	sym := &Symbol{}
	// Start of symbol (offset relative to segment).
	//
	//    0001:00000000
	rawStart := fields[0]
	start, err := parseSegmentOffset(rawStart)
	if err != nil {
		return nil, false, errors.WithStack(err)
	}
	sym.Start = start
	// Symbol name.
	//
	//    ?bar@@YIXH@Z
	sym.MangledName = fields[1]
	// TODO: demangle symbol name.
	// Address of symbol.
	//
	//    00401000
	rawAddr := fields[2]
	addr, err := strconv.ParseUint(rawAddr, 16, 64)
	if err != nil {
		return nil, false, errors.WithStack(err)
	}
	sym.Addr = addr
	// (optional) Symbol type.
	//
	//    f
	if len(fields) == 5 {
		rawSymbolType := fields[3]
		switch rawSymbolType {
		case "f":
			sym.IsFunc = true
		default:
			panic(fmt.Errorf("support for symbol type %q not yet implemented", rawSymbolType))
		}
	}
	// Object name.
	//
	//    baz.obj
	sym.ObjectName = fields[len(fields)-1]
	return sym, true, nil
}

// SegmentOffset specifies a segment relative offset.
//
// Example:
//    0001:00093247
type SegmentOffset struct {
	// Segment number.
	SegNum int
	// Offset in bytes from start of segment.
	Offset uint64
}

// parseSegmentOffset parses the string representation of the given segment
// offset.
func parseSegmentOffset(s string) (SegmentOffset, error) {
	var segOffset SegmentOffset
	// Segment number.
	parts := strings.Split(s, ":")
	rawSegNum := parts[0]
	segNum, err := strconv.ParseUint(rawSegNum, 16, 64)
	if err != nil {
		return SegmentOffset{}, errors.WithStack(err)
	}
	segOffset.SegNum = int(segNum)
	// Offset in bytes from start of segment.
	rawOffset := parts[1]
	offset, err := strconv.ParseUint(rawOffset, 16, 64)
	if err != nil {
		return SegmentOffset{}, errors.WithStack(err)
	}
	segOffset.Offset = offset
	return segOffset, nil
}
