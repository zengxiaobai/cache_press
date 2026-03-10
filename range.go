package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Range struct {
	Start int64
	End   int64
}

func writeWithRateLimit(w http.ResponseWriter, data []byte) (total int, err error) {
	if config.sendBytesPerInterval <= 0 || config.sendIntervalMs <= 0 {
		_, _ = w.Write(data)
		return
	}

	dataLen := len(data)
	offset := 0

	for offset < dataLen {
		chunkSize := config.sendBytesPerInterval
		if offset+chunkSize > dataLen {
			chunkSize = dataLen - offset
		}

		var n int
		n, err = w.Write(data[offset : offset+chunkSize])
		total += n
		if err != nil {
			return
		}
		offset += chunkSize

		if offset < dataLen {
			time.Sleep(time.Duration(config.sendIntervalMs) * time.Millisecond)
		}
	}
	return
}

func sendData(w http.ResponseWriter, data []byte) (total int, err error) {
	total, err = writeWithRateLimit(w, data)
	return
}

func parseRangeHeader(rangeHeader string, contentLength int64) ([]Range, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return nil, fmt.Errorf("unsupported range unit")
	}

	rangeSpec := strings.TrimPrefix(rangeHeader, "bytes=")
	if rangeSpec == "" {
		return nil, fmt.Errorf("empty range specification")
	}

	rangeParts := strings.Split(rangeSpec, ",")
	ranges := make([]Range, 0, len(rangeParts))

	for _, part := range rangeParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		hyphenPos := strings.Index(part, "-")
		if hyphenPos == -1 {
			return nil, fmt.Errorf("invalid range format: %s", part)
		}

		startStr := part[:hyphenPos]
		endStr := part[hyphenPos+1:]

		var start, end int64
		var err error

		if startStr == "" {
			if endStr == "" {
				return nil, fmt.Errorf("invalid range: both start and end are empty")
			}
			offset, err := strconv.ParseInt(endStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range offset: %s", endStr)
			}
			start = contentLength - offset
			if start < 0 {
				start = 0
			}
			end = contentLength - 1
		} else if endStr == "" {
			start, err = strconv.ParseInt(startStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range start: %s", startStr)
			}
			if start >= contentLength {
				return nil, fmt.Errorf("range start exceeds content length")
			}
			end = contentLength - 1
		} else {
			start, err = strconv.ParseInt(startStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range start: %s", startStr)
			}
			end, err = strconv.ParseInt(endStr, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range end: %s", endStr)
			}
		}

		if start < 0 || end < start {
			return nil, fmt.Errorf("invalid range: %d-%d", start, end)
		}
		if start >= contentLength {
			return nil, fmt.Errorf("range start exceeds content length")
		}
		if end >= contentLength {
			end = contentLength - 1
		}

		ranges = append(ranges, Range{Start: start, End: end})
	}

	if len(ranges) == 0 {
		return nil, fmt.Errorf("no valid ranges found")
	}

	return ranges, nil
}

func handleSingleRange(w http.ResponseWriter, r Range, responseBody []byte, contentType string, md5Sum string) {
	contentLength := int64(len(responseBody))

	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, contentLength))
	w.Header().Set("Content-Type", contentType)

	if !config.useChunkedTransfer {
		w.Header().Set("Content-Length", strconv.Itoa(int(r.End-r.Start+1)))
	}

	if md5Sum != "" {
		w.Header().Set("X-Content-MD5", md5Sum)
	}
	w.WriteHeader(http.StatusPartialContent)

	total, err := sendData(w, responseBody[r.Start:r.End+1])
	fmt.Printf("Single range response sent - Start: %d, End: %d, TotalSent: %d, Error: %v\n", r.Start, r.End, total, err)
}

func handleMultiRange(w http.ResponseWriter, ranges []Range, responseBody []byte, contentType string, md5Sum string) {
	contentLength := int64(len(responseBody))
	boundary := fmt.Sprintf("BOUNDARY_%d", time.Now().UnixNano())

	w.Header().Set("Content-Type", fmt.Sprintf("multipart/byteranges; boundary=%s", boundary))

	if !config.useChunkedTransfer && !config.multiRangeChunked {
		totalLength := calculateMultiRangeLength(ranges, contentLength, contentType, boundary)
		w.Header().Set("Content-Length", strconv.Itoa(totalLength))
	}

	if md5Sum != "" {
		w.Header().Set("X-Content-MD5", md5Sum)
	}
	w.WriteHeader(http.StatusPartialContent)

	for _, r := range ranges {
		_, _ = fmt.Fprintf(w, "--%s\r\n", boundary)
		_, _ = fmt.Fprintf(w, "Content-Type: %s\r\n", contentType)
		_, _ = fmt.Fprintf(w, "Content-Range: bytes %d-%d/%d\r\n", r.Start, r.End, contentLength)
		_, _ = fmt.Fprintf(w, "\r\n")
		total, err := sendData(w, responseBody[r.Start:r.End+1])
		fmt.Printf("Multi range part sent - Start: %d, End: %d, TotalSent: %d, Error: %v\n", r.Start, r.End, total, err)
		_, _ = fmt.Fprintf(w, "\r\n")
	}

	_, _ = fmt.Fprintf(w, "--%s--\r\n", boundary)
}

func calculateMultiRangeLength(ranges []Range, contentLength int64, contentType, boundary string) int {
	total := 0
	boundaryLen := len(boundary)
	contentTypeLen := len(contentType)

	for _, r := range ranges {
		partLength := 0
		partLength += len("--") + boundaryLen + len("\r\n")
		partLength += len("Content-Type: ") + contentTypeLen + len("\r\n")
		partLength += len(fmt.Sprintf("Content-Range: bytes %d-%d/%d\r\n", r.Start, r.End, contentLength))
		partLength += len("\r\n")
		partLength += int(r.End - r.Start + 1)
		partLength += len("\r\n")
		total += partLength
	}

	total += len("--") + boundaryLen + len("--\r\n")
	return total
}
