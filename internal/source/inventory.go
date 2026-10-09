package source

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

const (
	maxInventoryEntries = 300
	maxInventoryBytes   = 12000
)

// Inventory maps changed diff lines to the top-level Go declarations that
// enclose them. It is a navigation aid: declarations are matched by name only
// and a partial inventory says nothing about code it does not list.
type Inventory struct {
	Entries []InventoryEntry
	// Notes record per-file limitations such as parse failures or missing source.
	Notes []string
	// Skipped counts changed files that are not Go source.
	Skipped int
}

type InventoryEntry struct {
	Path string
	// BeforePath is the before-revision path when it differs from Path
	// (renames and copies) and the entry has before-side lines.
	BeforePath string
	Kind       string // func, method, type, const, var or file
	Name       string
	Receiver   string
	// Status is modified, added, deleted, ambiguous, changed (other revision
	// unavailable) or file-level.
	Status string
	Head   *LineRange
	Before *LineRange
	// HeadLines and BeforeLines list changed lines of file-level entries.
	HeadLines   []int
	BeforeLines []int
	Hunks       []int
	Partial     bool
}

// LineRange is a declaration's span; Changed reports whether changed diff
// lines on that revision fall inside it.
type LineRange struct {
	Start, End int
	Changed    bool
}

type declaration struct {
	kind, name, receiver string
	start, end           int
	changed              bool
	hunks                map[int]bool
}

func (d *declaration) key() string { return d.kind + "\x00" + d.receiver + "\x00" + d.name }

type inventorySide struct {
	decls     []*declaration
	available bool
	partial   bool
	// File-level changed lines outside any declaration.
	loose      []int
	looseHunks map[int]bool
}

// BuildInventory parses Go files touched by the diff from the collected
// snapshots. It returns nil when no snapshot is available or no Go file changed.
func BuildInventory(d *Diff, head, before *Snapshot) *Inventory {
	if d == nil || (head == nil && before == nil) {
		return nil
	}
	inv := &Inventory{}
	goFiles := 0
	for _, file := range d.Files {
		if !strings.HasSuffix(file.NewPath, ".go") && !strings.HasSuffix(file.OldPath, ".go") {
			inv.Skipped++
			continue
		}
		goFiles++
		inv.addFile(file, head, before)
	}
	if goFiles == 0 {
		return nil
	}
	return inv
}

func (inv *Inventory) addFile(file FileDiff, head, before *Snapshot) {
	display := file.NewPath
	if file.Status == "deleted" || display == "" {
		display = file.OldPath
	}
	if file.Binary {
		inv.Notes = append(inv.Notes, fmt.Sprintf("%q: binary change, not inventoried", display))
		return
	}
	headSide := inv.parseSide(display, "head", file.NewPath, file.Status != "deleted", head)
	beforeSide := inv.parseSide(display, "before", file.OldPath, file.Status != "added", before)
	if !headSide.available && !beforeSide.available {
		return
	}

	for i, hunk := range file.Hunks {
		number := i + 1
		if !hunk.Complete {
			inv.Notes = append(inv.Notes, fmt.Sprintf("%q: hunk %d is incomplete; some changed lines may be missing", display, number))
		}
		unmapped := 0
		for j, line := range hunk.Lines {
			switch line.Kind {
			case "add":
				headSide.mark(line.NewLine, number, line.Content)
			case "delete":
				if beforeSide.available {
					beforeSide.mark(line.OldLine, number, line.Content)
				} else if headSide.available && !headSide.markDeletion(hunk.Lines, j, number) {
					unmapped++
				}
			}
		}
		if unmapped > 0 {
			inv.Notes = append(inv.Notes, fmt.Sprintf("%q: %d deleted line(s) in hunk %d not mapped; before source unavailable", display, unmapped, number))
		}
	}

	partial := headSide.partial || beforeSide.partial
	var entries []InventoryEntry
	headByKey, beforeByKey := groupByKey(headSide.decls), groupByKey(beforeSide.decls)
	paired := map[*declaration]bool{}
	for _, h := range headSide.decls {
		befores := beforeByKey[h.key()]
		if len(headByKey[h.key()]) == 1 && len(befores) == 1 {
			b := befores[0]
			paired[b] = true
			if !h.changed && !b.changed {
				continue
			}
			entries = append(entries, newEntry(display, h, "modified", h, b, partial))
			continue
		}
		if !h.changed {
			continue
		}
		status := "added"
		switch {
		case len(headByKey[h.key()]) > 1 || len(befores) > 1:
			status = "ambiguous"
		case !beforeSide.available && file.Status != "added":
			status = "changed"
		}
		entries = append(entries, newEntry(display, h, status, h, nil, partial))
	}
	for _, b := range beforeSide.decls {
		if paired[b] || !b.changed {
			continue
		}
		status := "deleted"
		switch {
		case len(beforeByKey[b.key()]) > 1 || len(headByKey[b.key()]) > 1:
			status = "ambiguous"
		case !headSide.available && file.Status != "deleted":
			status = "changed"
		}
		entries = append(entries, newEntry(display, b, status, nil, b, partial))
	}
	if len(headSide.loose) > 0 || len(beforeSide.loose) > 0 {
		e := InventoryEntry{Path: display, Kind: "file", Status: "file-level", Partial: partial}
		e.HeadLines, e.BeforeLines = sortedLines(headSide.loose), sortedLines(beforeSide.loose)
		e.Hunks = sortedHunks(headSide.looseHunks, beforeSide.looseHunks)
		entries = append(entries, e)
	}
	if file.OldPath != "" && file.OldPath != display {
		for i := range entries {
			if entries[i].Before != nil || len(entries[i].BeforeLines) > 0 {
				entries[i].BeforePath = file.OldPath
			}
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].start() < entries[j].start() })
	inv.Entries = append(inv.Entries, entries...)
}

func newEntry(path string, d *declaration, status string, head, before *declaration, partial bool) InventoryEntry {
	e := InventoryEntry{Path: path, Kind: d.kind, Name: d.name, Receiver: d.receiver, Status: status, Partial: partial}
	var hunks []map[int]bool
	if head != nil {
		e.Head = &LineRange{Start: head.start, End: head.end, Changed: head.changed}
		hunks = append(hunks, head.hunks)
	}
	if before != nil {
		e.Before = &LineRange{Start: before.start, End: before.end, Changed: before.changed}
		hunks = append(hunks, before.hunks)
	}
	e.Hunks = sortedHunks(hunks...)
	return e
}

func (e InventoryEntry) start() int {
	switch {
	case e.Head != nil:
		return e.Head.Start
	case e.Before != nil:
		return e.Before.Start
	case len(e.HeadLines) > 0:
		return e.HeadLines[0]
	case len(e.BeforeLines) > 0:
		return e.BeforeLines[0]
	}
	return 0
}

func (inv *Inventory) parseSide(display, revision, path string, expected bool, snapshot *Snapshot) *inventorySide {
	side := &inventorySide{looseHunks: map[int]bool{}}
	if !expected || path == "" || !strings.HasSuffix(path, ".go") {
		return side
	}
	if snapshot == nil {
		return side
	}
	content, ok := snapshot.Files[path]
	if !ok {
		inv.Notes = append(inv.Notes, fmt.Sprintf("%q: %s source unavailable (excluded or not collected)", display, revision))
		return side
	}
	side.available = true
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, content, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		side.partial = true
		line := 0
		var list scanner.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			line = list[0].Pos.Line
		}
		inv.Notes = append(inv.Notes, fmt.Sprintf("%q: %s source failed to parse near line %d; entries may be incomplete", display, revision, line))
	}
	if parsed == nil {
		return side
	}
	// Ignore //line directives: diff and source tools use physical lines.
	lineOf := func(p token.Pos) int { return fset.PositionFor(p, false).Line }
	add := func(kind, name, receiver string, start, end token.Pos) {
		if !start.IsValid() || !end.IsValid() {
			return
		}
		side.decls = append(side.decls, &declaration{
			kind: kind, name: name, receiver: receiver,
			start: lineOf(start), end: lineOf(end), hunks: map[int]bool{},
		})
	}
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			start := decl.Pos()
			if decl.Doc != nil {
				start = decl.Doc.Pos()
			}
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				add("method", decl.Name.Name, receiverName(decl.Recv.List[0].Type), start, decl.End())
			} else {
				add("func", decl.Name.Name, "", start, decl.End())
			}
		case *ast.GenDecl:
			if decl.Tok == token.IMPORT {
				continue
			}
			kind := strings.ToLower(decl.Tok.String())
			grouped := decl.Lparen.IsValid()
			for _, spec := range decl.Specs {
				start, end := spec.Pos(), spec.End()
				var name string
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					name = spec.Name.Name
					if spec.Doc != nil {
						start = spec.Doc.Pos()
					}
				case *ast.ValueSpec:
					names := make([]string, len(spec.Names))
					for i, n := range spec.Names {
						names[i] = n.Name
					}
					name = strings.Join(names, ", ")
					if spec.Doc != nil {
						start = spec.Doc.Pos()
					}
				}
				if !grouped {
					start, end = decl.Pos(), decl.End()
					if decl.Doc != nil {
						start = decl.Doc.Pos()
					}
				}
				add(kind, name, "", start, end)
			}
		}
	}
	return side
}

func receiverName(expr ast.Expr) string {
	pointer := ""
	if star, ok := expr.(*ast.StarExpr); ok {
		pointer, expr = "*", star.X
	}
	switch t := expr.(type) {
	case *ast.IndexExpr:
		expr = t.X
	case *ast.IndexListExpr:
		expr = t.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return pointer + ident.Name
	}
	return pointer + "?"
}

func (s *inventorySide) enclosing(line int) *declaration {
	for _, d := range s.decls {
		if line >= d.start && line <= d.end {
			return d
		}
	}
	return nil
}

func (s *inventorySide) mark(line, hunk int, content string) {
	if !s.available || line < 1 {
		return
	}
	if d := s.enclosing(line); d != nil {
		d.changed = true
		d.hunks[hunk] = true
		return
	}
	// Blank separators between declarations are not worth listing.
	if strings.TrimSpace(content) == "" {
		return
	}
	s.loose = append(s.loose, line)
	s.looseHunks[hunk] = true
}

// markDeletion attributes a deleted line to a head declaration when the head
// lines on both sides of the deletion belong to that same declaration.
func (s *inventorySide) markDeletion(lines []DiffLine, index, hunk int) bool {
	previous, next := 0, 0
	for i := index - 1; i >= 0 && previous == 0; i-- {
		previous = lines[i].NewLine
	}
	for i := index + 1; i < len(lines) && next == 0; i++ {
		next = lines[i].NewLine
	}
	if previous == 0 || next == 0 {
		return false
	}
	d := s.enclosing(previous)
	if d == nil || d != s.enclosing(next) {
		return false
	}
	d.changed = true
	d.hunks[hunk] = true
	return true
}

func groupByKey(decls []*declaration) map[string][]*declaration {
	m := map[string][]*declaration{}
	for _, d := range decls {
		m[d.key()] = append(m[d.key()], d)
	}
	return m
}

func sortedLines(lines []int) []int {
	out := append([]int(nil), lines...)
	sort.Ints(out)
	return out
}

// compactLines renders sorted line numbers as "3,5-7".
func compactLines(lines []int) string {
	var parts []string
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && lines[j+1] <= lines[j]+1 {
			j++
		}
		if lines[i] == lines[j] {
			parts = append(parts, strconv.Itoa(lines[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", lines[i], lines[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

func sortedHunks(sets ...map[int]bool) []int {
	seen := map[int]bool{}
	var out []int
	for _, set := range sets {
		for h := range set {
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	sort.Ints(out)
	return out
}

// Render returns a bounded text listing. Paths are quoted because they are
// untrusted repository data.
func (inv *Inventory) Render() string {
	if inv == nil {
		return ""
	}
	var b strings.Builder
	for i, e := range inv.Entries {
		line := e.render()
		if i >= maxInventoryEntries || b.Len()+len(line) > maxInventoryBytes {
			fmt.Fprintf(&b, "[Inventory truncated: %d more entries omitted. Unlisted declarations may still have changed.]\n", len(inv.Entries)-i)
			break
		}
		b.WriteString(line)
	}
	for i, note := range inv.Notes {
		line := "Limitation: " + note + "\n"
		if b.Len()+len(line) > maxInventoryBytes+2000 {
			fmt.Fprintf(&b, "[%d more limitations omitted.]\n", len(inv.Notes)-i)
			break
		}
		b.WriteString(line)
	}
	if inv.Skipped > 0 {
		fmt.Fprintf(&b, "Not inventoried: %d changed non-Go file(s).\n", inv.Skipped)
	}
	return b.String()
}

func (e InventoryEntry) render() string {
	name := e.Name
	if e.Receiver != "" {
		name = "(" + e.Receiver + ")." + name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q", e.Path)
	if e.BeforePath != "" {
		fmt.Fprintf(&b, " before-path:%q", e.BeforePath)
	}
	b.WriteString(" " + e.Kind)
	if name != "" {
		b.WriteString(" " + name)
	}
	b.WriteString(" " + e.Status)
	var changedOn []string
	for _, side := range []struct {
		label string
		r     *LineRange
		lines []int
	}{{"head", e.Head, e.HeadLines}, {"before", e.Before, e.BeforeLines}} {
		if side.r != nil {
			fmt.Fprintf(&b, " %s:%d-%d", side.label, side.r.Start, side.r.End)
			if side.r.Changed {
				changedOn = append(changedOn, side.label)
			}
		}
		if len(side.lines) > 0 {
			fmt.Fprintf(&b, " %s-lines:%s", side.label, compactLines(side.lines))
		}
	}
	if e.Head != nil && e.Before != nil && len(changedOn) > 0 {
		b.WriteString(" changed-lines-in:" + strings.Join(changedOn, "+"))
	}
	if len(e.Hunks) > 0 {
		hunks := make([]string, len(e.Hunks))
		for i, h := range e.Hunks {
			hunks[i] = strconv.Itoa(h)
		}
		b.WriteString(" hunks:" + strings.Join(hunks, ","))
	}
	if e.Partial {
		b.WriteString(" (partial parse)")
	}
	b.WriteByte('\n')
	return b.String()
}
