package diff

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

func ParseValidLines(r io.Reader) (map[string]map[string]bool, error) {
	result := map[string]map[string]bool{}
	scanner := bufio.NewScanner(r)

	var file string
	var line int

	for scanner.Scan() {
		text := scanner.Text()

		if strings.HasPrefix(text, "+++ b/") {
			file = text[6:]
			continue
		}

		if strings.HasPrefix(text, "@@ ") {
			line = parseHunkStart(text)
			continue
		}

		if strings.HasPrefix(text, "+") && !strings.HasPrefix(text, "+++") {
			line++
			if file != "" {
				if result[file] == nil {
					result[file] = map[string]bool{}
				}
				result[file][strconv.Itoa(line)] = true
			}
			continue
		}

		if strings.HasPrefix(text, " ") {
			line++
		}
	}

	return result, scanner.Err()
}

func parseHunkStart(line string) int {
	// @@ -old,len +new,len @@
	plusIdx := strings.Index(line, "+")
	if plusIdx < 0 {
		return 0
	}
	rest := line[plusIdx+1:]
	endIdx := strings.IndexAny(rest, ", ")
	if endIdx < 0 {
		endIdx = strings.Index(rest, " @@")
		if endIdx < 0 {
			return 0
		}
	}
	n, err := strconv.Atoi(rest[:endIdx])
	if err != nil {
		return 0
	}
	return n - 1
}

func ValidLinesToJSON(validLines map[string]map[string]bool) ([]byte, error) {
	return json.Marshal(validLines)
}
