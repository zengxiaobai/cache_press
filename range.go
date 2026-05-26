package main

import (
	"fmt"
	"net/http"
	"os"
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

// sendDataChunked 以 chunked 传输方式分块发送数据，每块发送后 flush
// 用于 Transfer-Encoding: chunked 响应
func sendDataChunked(w http.ResponseWriter, data []byte) (total int, err error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// 不支持 flush，退化为普通发送
		return writeWithRateLimit(w, data)
	}

	dataLen := len(data)
	offset := 0
	chunkSize := config.sendBytesPerInterval
	if chunkSize <= 0 {
		chunkSize = 32 * 1024 // 默认 32KB 每块
	}

	for offset < dataLen {
		end := offset + chunkSize
		if end > dataLen {
			end = dataLen
		}

		var n int
		n, err = w.Write(data[offset:end])
		total += n
		if err != nil {
			return
		}

		// flush 触发 chunk 发送
		flusher.Flush()

		offset = end
		if offset < dataLen {
			time.Sleep(time.Duration(config.sendIntervalMs) * time.Millisecond)
		}
	}
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

func handleSingleRange(w http.ResponseWriter, r Range, responseBody []byte, contentType string, md5Sum string, useChunked bool, localFilePath string) {
	contentLength := int64(len(responseBody))

	writeDebugLog("[DEBUG] handleSingleRange: contentLength=%d, rangeStart=%d, rangeEnd=%d, localFilePath=%s\n",
		contentLength, r.Start, r.End, localFilePath)

	// 检查 range 是否超出 responseBody 范围
	if r.Start >= contentLength {
		writeDebugLog("[DEBUG] handleSingleRange: ERROR rangeStart >= contentLength, rangeStart=%d, contentLength=%d\n",
			r.Start, contentLength)
	}
	if r.End >= contentLength {
		writeDebugLog("[DEBUG] handleSingleRange: WARNING rangeEnd >= contentLength, rangeEnd=%d, contentLength=%d, adjusting\n",
			r.End, contentLength)
	}

	// 计算实际要发送的数据长度
	actualEnd := r.End
	if actualEnd >= contentLength {
		actualEnd = contentLength - 1
	}
	actualLen := int(actualEnd - r.Start + 1)
	writeDebugLog("[DEBUG] handleSingleRange: actual slice range [%d:%d], actualLen=%d, responseBody len=%d\n",
		r.Start, actualEnd+1, actualLen, len(responseBody))

	if localFilePath != "" {
		if fileInfo, err := os.Stat(localFilePath); err == nil {
			expectedSize := fileInfo.Size()
			if contentLength != expectedSize {
				writeDebugLog("[DEBUG] handleSingleRange: RANGE_MISMATCH expectedSize=%d, contentLength=%d, bodyLen=%d\n",
					expectedSize, contentLength, len(responseBody))
				logRangeMismatch(localFilePath, expectedSize, contentLength, r.Start, r.End, len(responseBody))
			}
		}
	}

	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, contentLength))
	w.Header().Set("Content-Type", contentType)

	rangeLen := int(r.End - r.Start + 1)
	if !useChunked {
		w.Header().Set("Content-Length", strconv.Itoa(rangeLen))
	} else {
		w.Header().Set("Transfer-Encoding", "chunked")
	}

	writeDebugLog("[DEBUG] handleSingleRange: set Content-Length=%d, Content-Range=bytes %d-%d/%d\n",
		rangeLen, r.Start, r.End, contentLength)

	if md5Sum != "" {
		w.Header().Set("X-Content-MD5", md5Sum)
	}
	w.WriteHeader(http.StatusPartialContent)

	// 实际切片
	dataSlice := responseBody[r.Start : r.End+1]
	writeDebugLog("[DEBUG] handleSingleRange: dataSlice len=%d, expected len=%d\n", len(dataSlice), rangeLen)

	total, err := sendData(w, dataSlice)
	writeDebugLog("[DEBUG] handleSingleRange: sent total=%d, err=%v\n", total, err)
	fmt.Printf("Single range response sent - Start: %d, End: %d, TotalSent: %d, Error: %v\n", r.Start, r.End, total, err)
}

func logRangeMismatch(localFilePath string, expectedSize int64, contentLength int64, rangeStart int64, rangeEnd int64, bodyLen int) {
	ts := time.Now().Format("2006-01-02T15:04:05.000000")
	msg := fmt.Sprintf("[%s] RANGE_MISMATCH file=%s expectedSize=%d contentLength=%d bodyLen=%d rangeStart=%d rangeEnd=%d\n",
		ts, localFilePath, expectedSize, contentLength, bodyLen, rangeStart, rangeEnd)
	writeDebugLog("%s", msg)
}

func handleMultiRange(w http.ResponseWriter, ranges []Range, responseBody []byte, contentType string, md5Sum string, useChunked bool, localFilePath string) {
	contentLength := int64(len(responseBody))
	boundary := fmt.Sprintf("BOUNDARY_%d", time.Now().UnixNano())

	w.Header().Set("Content-Type", fmt.Sprintf("multipart/byteranges; boundary=%s", boundary))

	if !config.useChunkedTransfer && !config.multiRangeChunked && !useChunked {
		totalLength := calculateMultiRangeLength(ranges, contentLength, contentType, boundary)
		w.Header().Set("Content-Length", strconv.Itoa(totalLength))
	} else {
		w.Header().Set("Transfer-Encoding", "chunked")
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
