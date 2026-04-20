package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
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
	StatusCode       int               `json:"status_code"`        // 响应状态码
	Error            string            `json:"error"`              // 发送过程中的错误
}

var accessLogWriter io.Writer
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

	// 使用 lumberjack.Logger 支持日志回滚和压缩
	accessLogWriter = &lumberjack.Logger{
		Filename:   config.logDir,
		MaxSize:    100,  // 每个日志文件最大 100MB
		MaxAge:     3,    // 日志保留 3 天
		MaxBackups: 500,  // 最多保留 10 个备份文件
		Compress:   true, // 压缩旧日志
		LocalTime:  true, // 使用本地时间
	}
}

// closeAccessLog 关闭访问日志
func closeAccessLog() {
	if accessLogWriter != nil {
		if closer, ok := accessLogWriter.(io.Closer); ok {
			closer.Close()
		}
	}
}

// logAccess 记录访问日志
func logAccess(requestID string, r *http.Request, w http.ResponseWriter, startTime time.Time, total int, statusCode int, err error) {
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
		StatusCode:       statusCode,
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
	if accessLogWriter != nil {
		accessLogWriter.Write(logBytes)
		accessLogWriter.Write([]byte("\n"))
		// 对于 lumberjack.Logger，Sync() 不是必需的，但如果是文件，我们可以尝试调用
		if syncWriter, ok := accessLogWriter.(interface{ Sync() error }); ok {
			syncWriter.Sync()
		}
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

func handlePreCompressedRange(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, etag string, traceID, method, host, url string, startTime time.Time) {
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
		// 获取状态码
		statusCode := http.StatusOK
		if wrapper, ok := w.(*responseWriterWrapper); ok {
			statusCode = wrapper.statusCode
		}

		logAccess(traceID, r, w, startTime, 0, statusCode, nil)
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
	setContentLanguageHeader(w, language)
	// 设置 Vary 头
	setVaryHeaders(w, encoding, language)

	if len(ranges) == 1 {
		handleSingleRange(w, ranges[0], compressedBody, contentType, md5Sum)
	} else {
		handleMultiRange(w, ranges, compressedBody, contentType, md5Sum)
	}

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, 0, statusCode, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩 Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Encoding: %s, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handlePreCompressedResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, etag string, traceID, method, host, url string, startTime time.Time) {
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
	setContentLanguageHeader(w, language)
	// 设置 Vary 头
	setVaryHeaders(w, encoding, language)

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
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, total, statusCode, err)

	bodyCompleteTime := time.Now()
	fmt.Printf("预压缩响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Encoding: %s, Start: %s, HeaderSent: %s, BodyComplete: %s, BodyLength: %d, TotalSent: %d, Error: %v\n",
		traceID, host, url, method, encoding,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(compressedBody), total, err)
}

func handleRangeRequest(w http.ResponseWriter, r *http.Request, responseBody []byte, language string, etag string, traceID, method, host, url string, startTime time.Time) {
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
		// 获取状态码
		statusCode := http.StatusOK
		if wrapper, ok := w.(*responseWriterWrapper); ok {
			statusCode = wrapper.statusCode
		}

		logAccess(traceID, r, w, startTime, 0, statusCode, nil)
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
	setVaryHeaders(w, "", language)
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
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, 0, statusCode, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("Range 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Ranges: %v, Start: %s, BodyComplete: %s\n",
		traceID, host, url, method, ranges,
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}

func handleNormalResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, etag string, useChunked bool, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"
	var requestURL string

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加 x-debug-req-url 头
	requestURL = buildRequestURL(r, host, url)
	w.Header().Set("X-Debug-Req-Url", requestURL)

	// 设置 Vary 头
	setVaryHeaders(w, encoding, language)

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	setContentLanguageHeader(w, language)

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

	if !useChunked {
		if encoding != "" {
			compressedBody := getPreCompressedBody(responseBody, encoding)
			w.Header().Set("Content-Length", strconv.Itoa(len(compressedBody)))
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		}
	} else {
		// 显式设置 chunked 传输编码
		w.Header().Set("Transfer-Encoding", "chunked")
	}

	w.WriteHeader(http.StatusOK)
	headerSendTime := time.Now()
	serveBodyWithDelay()

	var total int
	var err error
	if encoding != "" {
		if useChunked {
			// 使用流式压缩（chunk 流式压缩响应）
			err = streamCompressedBody(w, responseBody, encoding)
			total = len(responseBody)
		} else {
			// 使用预压缩
			compressedBody := getPreCompressedBody(responseBody, encoding)
			total, err = sendData(w, compressedBody)
		}
	} else {
		total, err = sendData(w, responseBody)
	}

	closeConnectionIfNeeded(w)

	// 打印响应头
	logResponseHeaders(w, traceID)
	// 记录访问日志
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, total, statusCode, err)

	bodyCompleteTime := time.Now()
	fmt.Printf("响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Start: %s, HeaderSent: %s, BodyComplete: %s,BodyLength: %d, TotalSent: %d, Error: %v\n",
		traceID, host, url, method,
		startTime.Format("2006-01-02 15:04:05.000"),
		headerSendTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		len(responseBody), total, err)
}

func handle304Response(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"
	var requestURL string

	// 打印请求头
	logRequestHeaders(r, traceID)

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	// 添加 x-debug-req-url 头
	requestURL = buildRequestURL(r, host, url)
	w.Header().Set("X-Debug-Req-Url", requestURL)

	// 设置 Vary 头
	setVaryHeaders(w, encoding, language)

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	setContentLanguageHeader(w, language)

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
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, 0, statusCode, nil)

	fmt.Printf("304 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Start: %s\n",
		traceID, host, url, method,
		startTime.Format("2006-01-02 15:04:05.000"))
}

type mockLocationMap struct {
	Orig     string `json:"orig"`
	Location string `json:"location"`
}

func handleMock302Redirect(w http.ResponseWriter, r *http.Request, locationMapJSON, language, traceID, method, host, url string, startTime time.Time) bool {
	var locationMaps []mockLocationMap
	if err := json.Unmarshal([]byte(locationMapJSON), &locationMaps); err != nil {
		fmt.Printf("无效的 X-Mock-302-Location-Map JSON 格式: %s, 错误: %v\n", locationMapJSON, err)
		http.Error(w, "Invalid JSON format", http.StatusBadRequest)
		return true
	}

	// 构建完整的请求 URL
	requestURL := buildRequestURL(r, host, url)

	var redirectLocation string
	for _, m := range locationMaps {
		if m.Orig == requestURL {
			redirectLocation = m.Location
			break
		}
	}

	if redirectLocation == "" {
		fmt.Printf("Mock 302 未命中 - Trace-ID: %s, RequestURL: %s\n", traceID, requestURL)
		return false
	}

	// 添加响应头文件中的内容
	addResponseHeaders(w)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	// 添加 X-Resp-Add-Header 请求头中的响应头
	addXRespAddHeaders(w, r)

	w.Header().Set("X-Debug-Req-Url", requestURL)
	w.Header().Set("Location", redirectLocation)
	w.WriteHeader(http.StatusFound)

	// 获取状态码
	statusCode := http.StatusFound
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, 0, statusCode, nil)

	fmt.Printf("Mock 302 重定向 - Trace-ID: %s, RequestURL: %s, Location: %s, Method: %s, Host: %s, Start: %s\n",
		traceID, requestURL, redirectLocation, method, host,
		startTime.Format("2006-01-02 15:04:05.000"))
	return true
}

// buildRequestURL 构建完整的请求 URL
func buildRequestURL(r *http.Request, host, url string) string {
	requestURL := url
	if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
		proto := r.Header.Get("X-Forwarded-Proto")
		if proto == "" {
			if r.TLS != nil {
				proto = "https"
			} else {
				proto = "http"
			}
		}
		requestURL = fmt.Sprintf("%s://%s%s", proto, host, requestURL)
	}

	// 清理 URL，去除可能的重复部分
	if strings.Contains(requestURL, "http://http://") {
		requestURL = strings.Replace(requestURL, "http://http://", "http://", 1)
	} else if strings.Contains(requestURL, "https://https://") {
		requestURL = strings.Replace(requestURL, "https://https://", "https://", 1)
	}

	return requestURL
}

func handleMockResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, etag string, traceID, method, host, url string, startTime time.Time, mockRespCode string) {
	// 解析状态码
	statusCode, err := strconv.Atoi(mockRespCode)
	var requestURL string
	if err != nil || statusCode < 100 || statusCode >= 599 {
		fmt.Printf("无效的 X-Mock-Resp-Code 值: %s，使用默认 304\n", mockRespCode)
		statusCode = http.StatusNotModified
	}

	contentType := "application/octet-stream"

	logRequestHeaders(r, traceID)

	addResponseHeaders(w)

	// 添加请求头到响应头中
	addRequestHeadersToResponse(w, r)

	w.Header().Set("Content-Type", contentType)
	setConnectionHeader(w)

	// 添加 x-debug-req-url 头
	requestURL = buildRequestURL(r, host, url)
	w.Header().Set("X-Debug-Req-Url", requestURL)

	// 添加 X-Resp-Add-Header 请求头中的响应头（优先级更高，可覆盖）
	addXRespAddHeaders(w, r)

	setVaryHeaders(w, encoding, language)

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	setContentLanguageHeader(w, language)

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
	// 获取状态码
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, total, statusCode, err)

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

// setContentLanguageHeader 设置 Content-Language 响应头
func setContentLanguageHeader(w http.ResponseWriter, language string) {
	if language != "" {
		w.Header().Set("Content-Language", language)
	}
}

// setVaryHeaders 设置 Vary 响应头，综合考虑 encoding、language 和配置的 varyHeaders
func setVaryHeaders(w http.ResponseWriter, encoding string, language string) {
	if len(config.varyHeaders) > 0 {
		w.Header().Set("Vary", strings.Join(config.varyHeaders, ", "))
		return
	}
	// 自动根据 encoding 和 language 推断 Vary
	var varyItems []string
	if encoding != "" {
		varyItems = append(varyItems, "Accept-Encoding")
	}
	if language != "" {
		varyItems = append(varyItems, "Accept-Language")
	}
	if len(varyItems) > 0 {
		w.Header().Set("Vary", strings.Join(varyItems, ", "))
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

func handleHeadResponse(w http.ResponseWriter, r *http.Request, responseBody []byte, encoding string, language string, etag string, traceID, method, host, url string, startTime time.Time) {
	contentType := "application/octet-stream"
	var requestURL string

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

	// 添加 x-debug-req-url 头
	requestURL = buildRequestURL(r, host, url)
	w.Header().Set("X-Debug-Req-Url", requestURL)

	// 设置 Vary 头
	setVaryHeaders(w, encoding, language)

	if encoding != "" {
		w.Header().Set("Content-Encoding", encoding)
	}
	setContentLanguageHeader(w, language)

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
	// 获取状态码
	statusCode := http.StatusOK
	if wrapper, ok := w.(*responseWriterWrapper); ok {
		statusCode = wrapper.statusCode
	}

	logAccess(traceID, r, w, startTime, 0, statusCode, nil)

	bodyCompleteTime := time.Now()
	fmt.Printf("HEAD 响应完成 - Trace-ID: %s, Host: %s, URL: %s, Method: %s, Content-Length: %d, Start: %s, HeaderSent: %s, BodyComplete: %s\n",
		traceID, host, url, method, len(responseBody),
		startTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"),
		bodyCompleteTime.Format("2006-01-02 15:04:05.000"))
}
