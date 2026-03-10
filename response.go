package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AccessLogEntry 访问日志条目
type AccessLogEntry struct {
	RequestID        string            `json:"request_id"`
	URL              string            `json:"url"`
	RequestHeaders   map[string]string `json:"request_headers"`
	ResponseHeaders  map[string]string `json:"response_headers"`
	RequestStartTime int64             `json:"request_start_time"` // 请求开始时间戳（毫秒）
	RequestLogTime   int64             `json:"request_log_time"`   // 记录日志的时间戳（毫秒）
	TotalSent        int               `json:"total_sent"`         // 实际发送的字节数
	Error            string            `json:"error"`              // 发送过程中的错误
}

var accessLogFile *os.File
var accessLogMutex sync.Mutex

// initAccessLog 初始化访问日志
func initAccessLog() {
	if config.logDir == "" {
		return
	}

	// 创建日志文件所在目录
	dir := filepath.Dir(config.logDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	var err error
	accessLogFile, err = os.OpenFile(config.logDir, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open access log file: %v", err)
	}
}

// closeAccessLog 关闭访问日志
func closeAccessLog() {
	if accessLogFile != nil {
		accessLogFile.Close()
	}
}

// logAccess 记录访问日志
func logAccess(requestID string, r *http.Request, w http.ResponseWriter, startTime time.Time, total int, err error) {
	if config.logDir == "" {
		return
	}

	entry := AccessLogEntry{
		RequestID:        requestID,
		URL:              r.URL.String(),
		RequestHeaders:   make(map[string]string),
		ResponseHeaders:  make(map[string]string),
		RequestStartTime: startTime.UnixMilli(),
		RequestLogTime:   time.Now().UnixMilli(),
		TotalSent:        total,
		Error:            "",
	}

	if err != nil {
		entry.Error = err.Error()
	}

	// 收集请求头
	if r.Header.Get("Range") != "" {
		entry.RequestHeaders["Range"] = r.Header.Get("Range")
	}
	if r.Header.Get("Accept-Encoding") != "" {
		entry.RequestHeaders["Accept-Encoding"] = r.Header.Get("Accept-Encoding")
	}

	// 收集响应头
	respHeaders := w.Header()
	if respHeaders.Get("Content-Range") != "" {
		entry.ResponseHeaders["Content-Range"] = respHeaders.Get("Content-Range")
	}
	if respHeaders.Get("Transfer-Encoding") != "" {
		entry.ResponseHeaders["Transfer-Encoding"] = respHeaders.Get("Transfer-Encoding")
	}
	if respHeaders.Get("Content-Length") != "" {
		entry.ResponseHeaders["Content-Length"] = respHeaders.Get("Content-Length")
	}
	if respHeaders.Get("Content-Encoding") != "" {
		entry.ResponseHeaders["Content-Encoding"] = respHeaders.Get("Content-Encoding")
	}

	// 序列化为 JSON
	logBytes, err := json.Marshal(entry)
	if err != nil {
		return
	}

	// 写入日志
	accessLogMutex.Lock()
	defer accessLogMutex.Unlock()
	if accessLogFile != nil {
		accessLogFile.Write(logBytes)
		accessLogFile.WriteString("\n")
		accessLogFile.Sync()
	}
}

// parseXRespAddHeader 解析 X-Resp-Add-Header 请求头
// 格式: "Header: Value"，支持多个用逗号分隔
func parseXRespAddHeader(headerValue string) ([]string, error) {
	if headerValue == "" {
		return nil, nil
	}

	// 去除两端的引号
	headerValue = strings.Trim(headerValue, "\"'")
	if headerValue == "" {
		return nil, nil
	}

	var result []string

	// 分割多个头（用逗号分隔）
	headerParts := strings.Split(headerValue, ",")
	for _, part := range headerParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// 查找冒号分隔符
		colonIndex := strings.Index(part, ":")
		if colonIndex == -1 {
			return nil, fmt.Errorf("无效的响应头格式: %s (应为 \"Header: Value\")", part)
		}

		name := strings.TrimSpace(part[:colonIndex])
		value := strings.TrimSpace(part[colonIndex+1:])

		if name != "" {
			result = append(result, name, value)
		}
	}

	return result, nil
}

// addXRespAddHeaders 从 X-Resp-Add-Header- 开头的请求头和 X-Resp-Add-Header 请求头中添加响应头
// 支持格式:
//
//	X-Resp-Add-Header-cache-control: max-age=10
//	X-Resp-Add-Header-test1: 2
//	X-Resp-Add-Header: "Cache-Control: max-age=10, Content-Type: text/plain"
func addXRespAddHeaders(w http.ResponseWriter, r *http.Request) {
	const prefix = "X-Resp-Add-Header-"

	// 处理 X-Resp-Add-Header- 开头的请求头
	for name, values := range r.Header {
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			continue
		}

		respHeaderName := name[len(prefix):]
		if respHeaderName == "" {
			continue
		}

		for _, value := range values {
			w.Header().Set(respHeaderName, value)
		}
	}

	// 处理 X-Resp-Add-Header 请求头
	if headerValue := r.Header.Get("X-Resp-Add-Header"); headerValue != "" {
		headers, err := parseXRespAddHeader(headerValue)
		if err == nil && len(headers) > 0 {
			for i := 0; i < len(headers); i += 2 {
				if i+1 < len(headers) {
					headerName := headers[i]
					headerValue := headers[i+1]
					w.Header().Set(headerName, headerValue)
				}
			}
		}
	}
}

// addResponseHeaders 添加响应头文件中的内容
func addResponseHeaders(w http.ResponseWriter) {
	for i := 0; i < len(config.respHeaders); i += 2 {
		if i+1 < len(config.respHeaders) {
			headerName := config.respHeaders[i]
			headerValue := config.respHeaders[i+1]
			w.Header().Set(headerName, headerValue)
		}
	}
}

// logRequestHeaders 打印请求头
func logRequestHeaders(r *http.Request, traceID string) {
	if config.logRequestHeaders {
		fmt.Printf("请求头 - Trace-ID: %s\n", traceID)
		for name, values := range r.Header {
			for _, value := range values {
				fmt.Printf("  %s: %s\n", name, value)
			}
		}
	}
}

// logResponseHeaders 打印响应头
func logResponseHeaders(w http.ResponseWriter, traceID string) {
	if config.logResponseHeaders {
		fmt.Printf("响应头 - Trace-ID: %s\n", traceID)
		for name, values := range w.Header() {
			for _, value := range values {
				fmt.Printf("  %s: %s\n", name, value)
			}
		}
	}
}

// addRequestHeadersToResponse 将所有请求头添加到响应头中，前缀为 X-Debug-ReqHdr-
func addRequestHeadersToResponse(w http.ResponseWriter, r *http.Request) {
	// Host header is special - it's not in r.Header, access via r.Host
	if r.Host != "" {
		w.Header().Add("X-Debug-ReqHdr-Host", r.Host)
	}

	for name, values := range r.Header {
		for _, value := range values {
			debugHeader := fmt.Sprintf("X-Debug-ReqHdr-%s", name)
			w.Header().Add(debugHeader, value)
		}
	}
}

func handlePreCompressedRange(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, etag string, traceID, method, host, url string, startTime time.Time) {
	compressedBody := getPreCompressedBody(responseBody, encoding)
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	ranges, err := parseRangeHeader(r.Header.Get("Range"), int64(len(compressedBody)))
	if err != nil {
		// 添加响应头文件中的内容
		addResponseHeaders(w)

		// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
		addXRespAddHeaders(w, r)

		// 添加请求头到响应头中
		addRequestHeadersToResponse(w, r)

		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(compressedBody)))
		fmt.Printf("Range 请求无效 - Trace-ID: %s, Error: %v %s\n", traceID, err, r.Header.Get("Range"))
		// 打印响应头
		logResponseHeaders(w, traceID)
		// 记录访问日志
		logAccess(traceID, r, w, startTime, 0, nil)
		return
	}

	var md5Sum string
	if config.enableHash {
		md5Sum = calculateRangeMD5(compressedBody, ranges)
		fmt.Printf("预压缩 Range 响应 MD5 - Trace-ID: %s, 范围数: %d, MD5: %s\n", traceID, len(ranges), md5Sum)
	}

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Encoding", encoding)
	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if len(ranges) == 1 {
		handleSingleRange(w, ranges[0], compressedBody, contentType, md5Sum)
	} else {
		handleMultiRange(w, ranges, compressedBody, contentType, md5Sum)
	}

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, 0, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩 Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Encoding: %s, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handlePreCompressedResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, etag string, traceID, method, host, url string, startTime time.Time) {
	compressedBody := getPreCompressedBody(responseBody, encoding)
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Encoding", encoding)
	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if !config.useChunkedTransfer {
		w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
	}

	if config.enableHash {
		md5Sum := calculateMD5(compressedBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("预压缩响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", traceID, len(compressedBody), md5Sum)
	}

	// 生成 etag 头
	if config.etag && etag != "" {
		// 设置 ETag 响应头
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", etag))
		fmt.Printf("预压缩响应 ETag - Trace-ID: %s, 大小: %d, ETag: %s\n", traceID, len(compressedBody), etag)
	}

	setConnectionHeader(w)
	w.WriteHeader(http.StatusOK)
	headerSendTime := time.Now()
	serveBodyWithDelay()
	total, err := sendData(w, compressedBody)
	fmt.Printf("sendData compressed: total=%d, err=%v\n", total, err)

	closeConnectionIfNeeded(w)

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, total, err)

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Encoding: %s, Start: %s, HeaderSent: %s, BodyComplete: %s, BodyLength: %d, TotalSent: %d, Error: %v\n",
		traceID, host, url, method, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(compressedBody), total, err)
}

func handleRangeRequest(w http.ResponseWriter, r *http.Request, responseBody []byte, etag string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	ranges, err := parseRangeHeader(r.Header.Get("Range"), int64(len(responseBody)))
	if err != nil {
		// 添加响应头文件中的内容
		addResponseHeaders(w)

		// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
		addXRespAddHeaders(w, r)

		// 添加请求头到响应头中
		addRequestHeadersToResponse(w, r)

		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(responseBody)))
		fmt.Printf("Range 请求无效 - Trace-ID: %s, Error: %v %s\n", traceID, err, r.Header.Get("Range"))
		// 打印响应头
		logResponseHeaders(w, traceID)
		// 记录访问日志
		logAccess(traceID, r, w, startTime, 0, nil)
		return
	}

	var md5Sum string
	if config.enableHash {
		md5Sum = calculateRangeMD5(responseBody, ranges)
		fmt.Printf("Range 响应 MD5 - Trace-ID: %s, 范围数: %d, MD5: %s\n", traceID, len(ranges), md5Sum)
	}

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	}
	bodyStartTime := time.Now()
	fmt.Printf("Range 响应开始 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyStartTime.Format("2006-01-02 15:04:05.000"))

	if len(ranges) == 1 {
		handleSingleRange(w, ranges[0], responseBody, contentType, md5Sum)
	} else {
		handleMultiRange(w, ranges, responseBody, contentType, md5Sum)
	}

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, 0, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handleNormalResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, etag string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		if encoding != "" {
			fmt.Printf("边压缩边响应 MD5 - Trace-ID: %s, 原始大小: %d, MD5: %s\n", traceID, len(responseBody), md5Sum)
		} else {
			fmt.Printf("MD5校验已启用，响应大小: %d, MD5: %s\n", len(responseBody), md5Sum)
		}
	}

	// 生成 etag 头
	if config.etag && etag != "" {
		// 设置 ETag 响应头
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", etag))
		fmt.Printf("ETag 已启用，响应大小: %d, ETag: %s\n", len(responseBody), etag)
	}

	if !config.useChunkedTransfer {
		if encoding != "" {
			compressedBody := getPreCompressedBody(responseBody, encoding)
			w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		}
	}

	w.WriteHeader(http.StatusOK)
	headerSendTime := time.Now()
	serveBodyWithDelay()

	var total int
	var err error
	if encoding != "" {
		compressedBody := getPreCompressedBody(responseBody, encoding)
		total, err = sendData(w, compressedBody)
	} else {
		total, err = sendData(w, responseBody)
	}

	closeConnectionIfNeeded(w)

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, total, err)

	bodyCompleteTime := time.Now()
	fmt.Printf("响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Start: %s, HeaderSent: %s, BodyComplete: %s,BodyLength: %d, TotalSent: %d, Error: %v\n",
		traceID, host, url, method,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(responseBody), total, err)
}

func handle304Response(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("304 响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", traceID, len(responseBody), md5Sum)
	}

	// 304 响应没有响应体，但可以保留 Content-Length 头
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))

	w.WriteHeader(http.StatusNotModified)

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, 0, nil)

	fmt.Printf("304 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Start: %s\n",
		traceID, host, url, method,
		startTime.Format("2006-01-02 15:04:05.000"))
}

func handleMockResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, etag string, traceID, method, host, url string, startTime time.Time, mockRespCode string) {
	// 解析状态码
	statusCode, err := strconv.Atoi(mockRespCode)
	if err != nil || statusCode < 100 || statusCode >= 599 {
		fmt.Printf("无效的 X-Mock-Resp-Code 值: %s，使用默认 304\n", mockRespCode)
		statusCode = http.StatusNotModified
	}

	contentType := "application/octet-stream"

	logRequestHeaders(r, traceID)

	addResponseHeaders(w)

	addXRespAddHeaders(w, r)

	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("Mock %d 响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", statusCode, traceID, len(responseBody), md5Sum)
	}

	// 检查是否需要遵循 x-press-size 等语义
	// 如果响应体不为空且不是 204 No Content，应该返回响应体
	hasResponseBody := len(responseBody) > 0 && statusCode != http.StatusNoContent

	// 对于 204 No Content，不应该设置 Content-Length
	if statusCode != http.StatusNoContent {
		if hasResponseBody {
			// 如果有响应体，设置正确的 Content-Length
			w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		} else {
			// 对于其他状态码，可以设置 Content-Length 为 0
			w.Header().Set("Content-Length", "0")
		}
	}

	w.WriteHeader(statusCode)

	// 如果需要返回响应体
	var total int
	if hasResponseBody {
		if encoding != "" {
			compressedBody := getPreCompressedBody(responseBody, encoding)
			total, err = sendData(w, compressedBody)
			fmt.Printf("sendData compressed: total=%d, err=%v\n", total, err)
		} else {
			total, err = sendData(w, responseBody)
			fmt.Printf("sendData: total=%d, err=%v\n", total, err)
		}
	}

	logResponseHeaders(w, traceID)
	logAccess(traceID, r, w, startTime, total, err)

	fmt.Printf("Mock 响应完成 - Status: %d, Trace-ID: %s, URL: %s, Method: %s, Host: %s, Start: %s, TotalSent: %d, Error: %v\n",
		statusCode, traceID, url, method, host,
		startTime.Format("2006-01-02 15:04:05.000"), total, err)
}

func setConnectionHeader(w http.ResponseWriter) {
	if config.keepAliveProb > 0 && rand.Float64() <= config.keepAliveProb {
		w.Header().Set("Connection", "keep-alive")
	} else {
		w.Header().Set("Connection", "close")
	}
}

func closeConnectionIfNeeded(w http.ResponseWriter) {
	if config.closeConnAfterBodyProb > 0 && rand.Float64() <= config.closeConnAfterBodyProb {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}
}

func handleHeadResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, etag string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))

	// 设置 Vary 头
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
	} else if encoding != "" {
		w.Header().Set("Vary", "Accept-Encoding")
	}

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}

	if config.enableHash {
		md5Sum := calculateMD5(responseBody)
		w.Header().Set("X-Content-MD5", md5Sum)
		fmt.Printf("HEAD 响应 MD5 - Trace-ID: %s, 大小: %d, MD5: %s\n", traceID, len(responseBody), md5Sum)
	}

	// 生成 etag 头
	if config.etag && etag != "" {
		// 设置 ETag 响应头
		w.Header().Set("ETag", fmt.Sprintf("\"%s\"", etag))
		fmt.Printf("HEAD 响应 ETag - Trace-ID: %s, 大小: %d, ETag: %s\n", traceID, len(responseBody), etag)
	}

	setConnectionHeader(w)
	w.WriteHeader(http.StatusOK)

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	logAccess(traceID, r, w, startTime, 0, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("HEAD 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Content-Length: %d, Start: %s, HeaderSent: %s, BodyComplete: %s\n",
		traceID, host, url, method, len(responseBody),
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}
