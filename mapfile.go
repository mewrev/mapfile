// Package mapfile provides access to symbol map files (MAP file format)
// produced by linkers.
package mapfile

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mewkiz/pkg/term"
	"github.com/pkg/errors"
)

var (
	// dbg is a logger with the "pdb:" prefix which logs debug messages to standard
	// error.
	dbg = log.New(os.Stderr, term.CyanBold("pdb:")+" ", 0)
	// warn is a logger with the "pdb:" prefix which logs warning messages to
	// standard error.
	warn = log.New(os.Stderr, term.RedBold("pdb:")+" ", 0)
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
	// Read lines.
	lines, err := readLines(r)
	if err != nil {
		return nil, errors.WithStack(err)
	}

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
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		// Name of linker output.
		case i == 0:
			m.Name = line // first line is linker output name.
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
			// Preferred load address is 00400000
			rawBaseAddr := line[len("Preferred load address is "):]
			baseAddr, err := strconv.ParseUint(rawBaseAddr, 16, 64)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.BaseAddr = baseAddr
		// List of sections.
		// Start         Length     Name                   Class
		case hasFields(line, []string{"Start", "Length", "Name", "Class"}):
			sects, n, err := parseSections(lines[i:])
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.Sects = append(m.Sects, sects...)
			i += n - 1
		// List of symbols.
		// Address         Publics by Value              Rva+Base   Lib:Object
		case hasFields(line, []string{"Address", "Publics", "by", "Value", "Rva+Base", "Lib:Object"}):
			fallthrough
		case strings.HasPrefix(line, "Static symbols"):
			syms, n, err := parseSymbols(lines[i:])
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.Syms = append(m.Syms, syms...)
			i += n - 1
		// Entry point.
		case strings.HasPrefix(line, "entry point at"):
			// entry point at        0001:000f0290
			rawEntry := strings.TrimSpace(strings.TrimPrefix(line, "entry point at"))
			entry, err := parseSegmentOffset(rawEntry)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			m.Entry = entry
		case strings.HasPrefix(line, "FIXUPS:"):
			// ignore.
		case len(line) == 0:
			// skip empty lines.
		default:
			warn.Printf("support for line prefix %q not yet implemented", line)
		}
	}
	return m, nil
}

// hasFields reports whether the given line contains the specified fields, as
// separated by whitespace.
func hasFields(line string, fields []string) bool {
	got := strings.Fields(line)
	if len(fields) != len(got) {
		return false
	}
	for i := range fields {
		want := fields[i]
		if want != got[i] {
			return false
		}
	}
	return true
}

// parseSections parses a list of sections from the given lines, terminated by a
// blank line.
func parseSections(lines []string) (sects []*Section, n int, err error) {
	// Skip header.
	n++
	// Parse list of sections.
	for ; n < len(lines); n++ {
		line := lines[n]
		if len(line) == 0 {
			// End of sections list reached.
			break
		}
		// 0001:00000000 001012c6H .text                   CODE
		sect, err := parseSection(line)
		if err != nil {
			return nil, n, errors.WithStack(err)
		}
		sects = append(sects, sect)
	}
	return sects, n, nil
}

// parseSymbols parses a list of symbols from the given lines, terminated by a
// blank line.
func parseSymbols(lines []string) (syms []*Symbol, n int, err error) {
	// Parse header.
	header := lines[n]
	isStatic := strings.HasPrefix(header, "Static symbols")
	n++
	// Parse empty line between header and list of symbols.
	emptyLine := lines[n]
	if len(emptyLine) != 0 {
		return nil, n, errors.Errorf("unexpected line between header and list of symbols; expected empty line, got %q", emptyLine)
	}
	n++
	// Parse list of symbols.
	for ; n < len(lines); n++ {
		line := lines[n]
		if len(line) == 0 {
			// End of symbols list reached.
			break
		}
		// 0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
		sym, err := parseSymbol(line)
		if err != nil {
			return nil, n, errors.WithStack(err)
		}
		sym.IsStatic = isStatic
		syms = append(syms, sym)
	}
	return syms, n, nil
}

// Section tracks section linkage information.
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
func parseSection(s string) (*Section, error) {
	// Example:
	//
	//    0001:00000000 001012c6H .text                   CODE
	fields := strings.Fields(s)
	sect := &Section{}
	// Start of section (offset relative to segment).
	//
	//    0001:00000000
	rawStart := fields[0]
	start, err := parseSegmentOffset(rawStart)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	sect.Start = start
	// Size in bytes.
	//
	//    001012c6H
	rawSize := strings.TrimSuffix(fields[1], "H")
	size, err := strconv.ParseUint(rawSize, 16, 64)
	if err != nil {
		return nil, errors.WithStack(err)
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
	return sect, nil
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
func parseSymbol(s string) (*Symbol, error) {
	// Example:
	//
	//    0001:00000000       ?bar@@YIXH@Z               00401000 f baz.obj
	sym := &Symbol{}
	fields := strings.Fields(s)
	// Start of symbol (offset relative to segment).
	//
	//    0001:00000000
	rawStart := fields[0]
	start, err := parseSegmentOffset(rawStart)
	if err != nil {
		return nil, errors.WithStack(err)
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
		return nil, errors.WithStack(err)
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
	return sym, nil
}

// SegmentOffset specifies a segment relative offset.
type SegmentOffset struct {
	// Segment number.
	SegNum int
	// Offset in bytes from start of segment.
	Offset uint64
}

// parseSegmentOffset parses the string representation of the given segment
// offset.
func parseSegmentOffset(s string) (SegmentOffset, error) {
	// Example:
	//
	//    0001:00093247
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

// readLines reads and returns the lines of r, trimming spaces of each line.
func readLines(r io.Reader) ([]string, error) {
	s := bufio.NewScanner(r)
	var lines []string
	for s.Scan() {
		line := s.Text()
		line = strings.TrimSpace(line)
		lines = append(lines, line)
	}
	if err := s.Err(); err != nil {
		return nil, errors.WithStack(err)
	}
	return lines, nil
}
