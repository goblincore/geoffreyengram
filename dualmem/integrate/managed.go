package integrate

import (
	"fmt"
	"strings"
)

func ReplaceManagedBlock(input, begin, end, body string) (string, error) {
	block, err := renderManagedBlock(begin, end, body)
	if err != nil {
		return "", err
	}
	rangeInfo, err := findManagedBlock(input, begin, end)
	if err != nil {
		return "", err
	}
	if !rangeInfo.found {
		prefix := input
		if prefix != "" && !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		return prefix + block + "\n", nil
	}
	suffix := input[rangeInfo.end:]
	if rangeInfo.endedWithNewline {
		block += "\n"
	}
	return input[:rangeInfo.start] + block + suffix, nil
}

func RemoveManagedBlock(input, begin, end string) (string, error) {
	if err := validateMarkers(begin, end); err != nil {
		return "", err
	}
	rangeInfo, err := findManagedBlock(input, begin, end)
	if err != nil {
		return "", err
	}
	if !rangeInfo.found {
		return input, nil
	}
	return input[:rangeInfo.start] + input[rangeInfo.end:], nil
}

type managedRange struct {
	found            bool
	start            int
	end              int
	endedWithNewline bool
}

type markerOccurrence struct {
	start            int
	end              int
	endedWithNewline bool
}

func findManagedBlock(input, begin, end string) (managedRange, error) {
	if err := validateMarkers(begin, end); err != nil {
		return managedRange{}, err
	}
	begins := exactLineOccurrences(input, begin)
	ends := exactLineOccurrences(input, end)
	if len(begins) == 0 && len(ends) == 0 {
		return managedRange{}, nil
	}
	if len(begins) != 1 || len(ends) != 1 {
		return managedRange{}, fmt.Errorf("managed markers must each occur exactly once")
	}
	if ends[0].start <= begins[0].start {
		return managedRange{}, fmt.Errorf("managed markers overlap or are out of order")
	}
	return managedRange{
		found:            true,
		start:            begins[0].start,
		end:              ends[0].end,
		endedWithNewline: ends[0].endedWithNewline,
	}, nil
}

func renderManagedBlock(begin, end, body string) (string, error) {
	if err := validateMarkers(begin, end); err != nil {
		return "", err
	}
	if len(exactLineOccurrences(body, begin)) != 0 || len(exactLineOccurrences(body, end)) != 0 {
		return "", fmt.Errorf("managed body contains a marker line")
	}
	block := begin + "\n"
	if body != "" {
		block += body
		if !strings.HasSuffix(body, "\n") {
			block += "\n"
		}
	}
	return block + end, nil
}

func validateMarkers(begin, end string) error {
	if begin == "" || end == "" || begin == end {
		return fmt.Errorf("managed markers must be non-empty and distinct")
	}
	if strings.ContainsAny(begin, "\r\n") || strings.ContainsAny(end, "\r\n") {
		return fmt.Errorf("managed markers must be single lines")
	}
	return nil
}

func exactLineOccurrences(input, marker string) []markerOccurrence {
	var occurrences []markerOccurrence
	for start := 0; start <= len(input); {
		relativeEnd := strings.IndexByte(input[start:], '\n')
		lineEnd := len(input)
		spanEnd := len(input)
		withNewline := false
		if relativeEnd >= 0 {
			lineEnd = start + relativeEnd
			spanEnd = lineEnd + 1
			withNewline = true
		}
		if input[start:lineEnd] == marker {
			occurrences = append(occurrences, markerOccurrence{start: start, end: spanEnd, endedWithNewline: withNewline})
		}
		if relativeEnd < 0 {
			break
		}
		start = spanEnd
	}
	return occurrences
}
