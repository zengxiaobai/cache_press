package main

import (
	"cache_press/pkg/buffer"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// respCacheItem 存储响应体和对应的 etag
type respCacheItem struct {
	body []byte
	etag string
}

// fileETagCacheItem 存储文件 ETag 缓存
type fileETagCacheItem struct {
	etag        string    // 文件内容的 MD5 ETag
	modTime     time.Time // 文件最后修改时间
	size        int64     // 文件大小
}

var (
	respCache      = make(map[int]respCacheItem)
	respCacheMutex sync.RWMutex
	
	// fileETagCache 缓存本地文件的 ETag，key 为文件路径
	fileETagCache      = make(map[string]*fileETagCacheItem)
	fileETagCacheMutex sync.RWMutex
)

// getFileETag 获取文件的 ETag，如果文件变化了则重新计算
// 使用文件的修改时间和大小来判断文件是否变化
func getFileETag(filePath string) (string, error) {
	// 获取文件信息
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("获取文件信息失败: %w", err)
	}
	
	modTime := fileInfo.ModTime()
	size := fileInfo.Size()
	
	// 检查缓存
	fileETagCacheMutex.RLock()
	cached, exists := fileETagCache[filePath]
	fileETagCacheMutex.RUnlock()
	
	// 如果缓存存在且文件未变化，返回缓存的 ETag
	if exists && cached.modTime.Equal(modTime) && cached.size == size {
		return cached.etag, nil
	}
	
	// 文件变化或缓存不存在，重新计算 ETag
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}
	
	hash := md5.Sum(fileContent)
	etag := hex.EncodeToString(hash[:])
	
	// 更新缓存
	fileETagCacheMutex.Lock()
	fileETagCache[filePath] = &fileETagCacheItem{
		etag:    etag,
		modTime: modTime,
		size:    size,
	}
	fileETagCacheMutex.Unlock()
	
	return etag, nil
}

// generateSelfSignedCert 生成自签证书
func generateSelfSignedCert(certFile, keyFile, domain string) error {
	// 生成私钥
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("生成私钥失败: %w", err)
	}

	// 创建证书模板
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: domain,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// 生成证书
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("生成证书失败: %w", err)
	}

	// 保存证书
	certOut, err := os.Create(certFile)
	if err != nil {
		return fmt.Errorf("创建证书文件失败: %w", err)
	}
	defer certOut.Close()

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
	if _, err := certOut.Write(certPEM); err != nil {
		return fmt.Errorf("写入证书文件失败: %w", err)
	}

	// 保存私钥
	keyOut, err := os.Create(keyFile)
	if err != nil {
		return fmt.Errorf("创建私钥文件失败: %w", err)
	}
	defer keyOut.Close()

	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("编码私钥失败: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyBytes,
	})
	if _, err := keyOut.Write(keyPEM); err != nil {
		return fmt.Errorf("写入私钥文件失败: %w", err)
	}

	fmt.Printf("自签证书已生成: %s, %s\n", certFile, keyFile)
	return nil
}

// parseFileSize 从文件名中解析文件大小
// 支持格式: /path/to/20GB, /path/to/100MB 等
func parseFileSize(filePath string) (int64, error) {
	// 获取文件名
	filename := filepath.Base(filePath)

	// 匹配数字+单位的模式
	re := regexp.MustCompile(`(\d+)([GMK]B?)`)
	matches := re.FindStringSubmatch(filename)

	if len(matches) != 3 {
		return 0, fmt.Errorf("无效的文件名格式，无法解析大小: %s", filename)
	}

	// 解析数字部分
	sizeStr := matches[1]
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("解析文件大小失败: %w", err)
	}

	// 解析单位部分
	unit := strings.ToUpper(matches[2])
	switch unit {
	case "GB", "G":
		size *= 1024 * 1024 * 1024
	case "MB", "M":
		size *= 1024 * 1024
	case "KB", "K":
		size *= 1024
	default:
		return 0, fmt.Errorf("不支持的单位: %s", unit)
	}

	return size, nil
}

// generateFile 生成指定大小的文件
func generateFile(filePath string, size int64) error {
	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 创建文件
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()

	// 使用 1MB 的缓冲区生成文件内容
	const bufferSize = 1024 * 1024
	buffer := make([]byte, bufferSize)
	pattern := "0123456789"
	for i := range buffer {
		buffer[i] = pattern[i%len(pattern)]
	}

	// 写入文件
	var written int64
	for written < size {
		writeSize := bufferSize
		if written+int64(writeSize) > size {
			writeSize = int(size - written)
		}
		n, err := f.Write(buffer[:writeSize])
		if err != nil {
			return fmt.Errorf("写入文件失败: %w", err)
		}
		written += int64(n)
	}

	fmt.Printf("文件已生成: %s, 大小: %d 字节\n", filePath, size)
	return nil
}

func serverGetRespSize(r *http.Request) int {
	sizeHeader := r.Header.Get("x-press-size")

	var responseSize int
	if sizeHeader != "" {
		parsedSize, err := strconv.Atoi(sizeHeader)
		if err == nil {
			responseSize = parsedSize
		} else {
			responseSize = defaultRespSize
		}
	} else {
		responseSize = defaultRespSize
	}
	return responseSize
}

func serveHeaderWithDelay() {
	if config.delayRespHdr > 0 {
		delay := config.delayRespHdr
		if config.delayRespHdrRandom > 0 {
			delay += mrand.Intn(config.delayRespHdrRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

// acceptItem 表示 Accept-Encoding / Accept-Language 中的一个条目
type acceptItem struct {
	value string
	q     float64
}

// negotiateEncoding 解析 Accept-Encoding 头并选择服务器支持的最佳编码
// 支持带 q 值的场景（如 "gzip;q=1.0, br;q=0.5"）和多编码逗号分隔的场景（如 "gzip, br"）
// 按服务器偏好优先级遍历，选择客户端 q 值最高的编码
// q=0 表示不接受该编码，* 表示接受任意编码
func negotiateEncoding(ae string) string {
	if ae == "" {
		return ""
	}

	// 服务器支持的编码，按优先级排序（br 优先于 gzip）
	serverSupported := []string{"br", "gzip"}

	// 解析 Accept-Encoding 头
	items := parseAcceptList(ae)

	// 构建 q 值映射：编码 -> 最高 q 值
	qMap := make(map[string]float64)
	var wildcardQ float64 = -1 // -1 表示没有通配符
	for _, item := range items {
		key := strings.ToLower(item.value)
		if key == "*" {
			wildcardQ = item.q
			continue
		}
		// 取同一编码的最高 q 值
		if existing, ok := qMap[key]; !ok || item.q > existing {
			qMap[key] = item.q
		}
	}

	// 在客户端可接受的编码中（q > 0），按服务器偏好优先级选择
	var bestEncoding string
	var bestQ float64 = -1

	for _, supported := range serverSupported {
		q, ok := qMap[supported]
		if !ok {
			// 客户端未明确指定此编码，检查通配符
			if wildcardQ > 0 {
				q = wildcardQ
			} else {
				continue
			}
		}
		if q <= 0 {
			// 客户端明确拒绝此编码（q=0）
			continue
		}
		if q > bestQ {
			bestQ = q
			bestEncoding = supported
		}
	}

	return bestEncoding
}

// negotiateLanguage 解析 Accept-Language 头并选择 q 值最高的语言
// 不需要服务器配置语言列表，直接使用客户端携带的语言
// 支持带 q 值的场景（如 "zh-CN;q=1.0, en;q=0.5"）和多语言逗号分隔的场景（如 "zh-CN, en"）
// 返回客户端 q 值最高且 q > 0 的语言，q=0 表示不接受该语言
func negotiateLanguage(al string) string {
	if al == "" {
		return ""
	}

	// 解析 Accept-Language 头
	items := parseAcceptList(al)

	// 选择 q 值最高且 q > 0 的语言
	var bestLang string
	var bestQ float64 = -1

	for _, item := range items {
		if item.q <= 0 || item.value == "*" {
			continue
		}
		if item.q > bestQ {
			bestQ = item.q
			bestLang = item.value
		}
	}

	return bestLang
}

// parseAcceptList 解析 Accept-Encoding / Accept-Language 头值
// 格式示例:
//
//	"gzip"
//	"gzip, br"
//	"gzip;q=1.0, br;q=0.5"
//	"zh-CN;q=1.0, en;q=0.5"
//	"*;q=0, br"
func parseAcceptList(headerValue string) []acceptItem {
	var items []acceptItem

	// 按逗号分割各个条目
	parts := strings.Split(headerValue, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// 按分号分割值和参数
		segments := strings.Split(part, ";")
		value := strings.TrimSpace(segments[0])

		// 默认 q 值为 1.0
		q := 1.0

		// 解析参数（如 q=0.5）
		for _, seg := range segments[1:] {
			seg = strings.TrimSpace(seg)
			if strings.HasPrefix(strings.ToLower(seg), "q=") {
				if val, err := strconv.ParseFloat(seg[2:], 64); err == nil {
					q = val
				}
			}
		}

		items = append(items, acceptItem{value: value, q: q})
	}

	return items
}

func serveBodyWithDelay() {
	if config.delayRespBody > 0 {
		delay := config.delayRespBody
		if config.delayRespBodyRandom > 0 {
			delay += mrand.Intn(config.delayRespBodyRandom)
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
}

func getTraceID(r *http.Request) string {
	if r.Header.Get(config.ReqIDHdrName) != "" {
		return r.Header.Get(config.ReqIDHdrName)
	}
	return "unknown"
}

func createRandomRespBody(size int) []byte {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()_+-=[]{}|;:,.<>?"
	charsetLen := len(charset)

	buf := buffer.GetIoBuffer(size)
	defer buffer.PutIoBuffer(buf)

	for i := 0; i < size; i++ {
		buf.WriteByte(charset[mrand.Intn(charsetLen)])
	}

	return buf.Bytes()
}

func createRespBodyCont(size int) []byte {
	if config.useRandomContent {
		return createRandomRespBody(size)
	}

	const pattern = "1234567890abcdefghij"
	patternLen := len(pattern)

	buf := buffer.GetIoBuffer(size)
	defer buffer.PutIoBuffer(buf)

	fullTimes := size / patternLen
	remainder := size % patternLen

	for i := 0; i < fullTimes; i++ {
		buf.WriteString(pattern)
	}

	if remainder > 0 {
		buf.WriteString(pattern[:remainder])
	}

	return buf.Bytes()
}

func genRespBody(responseSize int) ([]byte, string) {
	var responseBody []byte
	var etag string
	if config.cacheResp {
		respCacheMutex.RLock()
		var ok bool
		var item respCacheItem
		item, ok = respCache[responseSize]
		respCacheMutex.RUnlock()
		if !ok {
			newBody := createRespBodyCont(responseSize)
			newEtag := ""
			if config.etag {
				// 使用 MD5 算法计算响应内容的哈希值
				hash := md5.Sum(newBody)
				// 将哈希值转换为十六进制字符串
				newEtag = hex.EncodeToString(hash[:])
			}
			respCacheMutex.Lock()
			defer respCacheMutex.Unlock()
			if _, ok := respCache[responseSize]; !ok {
				respCache[responseSize] = respCacheItem{
					body: newBody,
					etag: newEtag,
				}
			}
			responseBody = newBody
			etag = newEtag
		} else {
			responseBody = item.body
			etag = item.etag
		}
	} else {
		responseBody = createRespBodyCont(responseSize)
		if config.etag {
			// 使用 MD5 算法计算响应内容的哈希值
			hash := md5.Sum(responseBody)
			// 将哈希值转换为十六进制字符串
			etag = hex.EncodeToString(hash[:])
		}
	}
	return responseBody, etag
}

// responseWriterWrapper 包装 http.ResponseWriter 以捕获状态码
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// prepareRequestContext 准备请求上下文，包括删除指定请求头、延迟响应头、获取跟踪ID等
func prepareRequestContext(w http.ResponseWriter, r *http.Request) (traceID, method, host, url string, startTime time.Time) {
	// 删除配置中指定的请求头
	for _, hdr := range config.delReqHdrs {
		r.Header.Del(hdr)
	}

	serveHeaderWithDelay()

	traceID = getTraceID(r)
	method = r.Method
	host = r.Host
	url = r.URL.String()
	startTime = time.Now()

	return traceID, method, host, url, startTime
}

// parseChunkedHeader 解析 X-Use-Chunked-Transfer 请求头，返回是否使用 chunked 传输
func parseChunkedHeader(r *http.Request) bool {
	useChunked := config.useChunkedTransfer
	if chunkedHeader := r.Header.Get("X-Use-Chunked-Transfer"); chunkedHeader != "" {
		switch chunkedHeader {
		case "true", "1":
			useChunked = true
		case "false", "0":
			useChunked = false
		}
	}
	return useChunked
}

// handleFileError 处理文件相关错误
func handleFileError(w http.ResponseWriter, r *http.Request, statusCode int, errMsg string, traceID string, startTime time.Time) {
	wrapper := &responseWriterWrapper{
		ResponseWriter: w,
		statusCode:     statusCode,
	}
	http.Error(wrapper, errMsg, statusCode)
	logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, fmt.Errorf(errMsg))
}

// processRequestWithContent 使用指定的内容处理请求
func processRequestWithContent(w http.ResponseWriter, r *http.Request, content []byte, encoding, language, etag string, useChunked bool, traceID, method, host, url string, startTime time.Time) {
	// 处理 X-Mock-302-Location-Map 请求头
	if locationMap := r.Header.Get("X-Mock-302-Location-Map"); locationMap != "" {
		handled := handleMock302Redirect(w, r, locationMap, language, traceID, method, host, url, startTime)
		if handled {
			return
		}
	}

	// 处理 X-Mock-Resp-Code 请求头
	if mockRespCode := r.Header.Get("X-Mock-Resp-Code"); mockRespCode != "" {
		handleMockResponse(w, r, content, encoding, language, etag, traceID, method, host, url, startTime, mockRespCode)
		return
	}

	if method == "HEAD" {
		handleHeadResponse(w, r, content, encoding, language, etag, traceID, method, host, url, startTime)
		return
	}

	// 处理预压缩
	if config.preCompress && encoding != "" {
		if r.Header.Get("Range") != "" {
			handlePreCompressedRange(w, r, content, encoding, language, etag, traceID, method, host, url, startTime)
		} else {
			handlePreCompressedResponse(w, r, content, encoding, language, etag, traceID, method, host, url, startTime)
		}
		return
	}

	// 处理 Range 请求
	if r.Header.Get("Range") != "" {
		handleRangeRequest(w, r, content, language, etag, traceID, method, host, url, startTime)
		return
	}

	// 处理正常响应
	handleNormalResponse(w, r, content, encoding, language, etag, useChunked, traceID, method, host, url, startTime)
}

// handleLocalFileRequest 处理 X-Req-Local-File 请求头指定的本地文件请求
// 如果请求包含 X-Req-Local-File 头且文件存在，则处理该请求并返回 true
// 否则返回 false，调用方应继续处理其他逻辑
func handleLocalFileRequest(w http.ResponseWriter, r *http.Request, responseBody []byte, etag string, lastModified time.Time, traceID, method, host, url string, startTime time.Time) bool {
	// 获取 X-Req-Local-File 请求头
	localFilePath := r.Header.Get("X-Req-Local-File")
	if localFilePath == "" {
		return false
	}

	// 检查文件是否存在
	if _, err := os.Stat(localFilePath); os.IsNotExist(err) {
		handleFileError(w, r, http.StatusNotFound, fmt.Sprintf("文件不存在: %s", localFilePath), traceID, startTime)
		return true
	}

	// 读取文件内容到内存
	fileContent, err := os.ReadFile(localFilePath)
	if err != nil {
		handleFileError(w, r, http.StatusInternalServerError, fmt.Sprintf("读取文件失败: %v", err), traceID, startTime)
		return true
	}

	// 协商编码（压缩）
	ae := r.Header.Get("Accept-Encoding")
	encoding := negotiateEncoding(ae)

	// 协商语言
	al := r.Header.Get("Accept-Language")
	language := negotiateLanguage(al)

	// 检查请求头是否控制 chunked 传输
	useChunked := parseChunkedHeader(r)

	// 动态获取 etag（如果文件变化了会重新计算）
	var fileEtag string
	if config.etag {
		fileEtag, err = getFileETag(localFilePath)
		if err != nil {
			handleFileError(w, r, http.StatusInternalServerError, fmt.Sprintf("计算 ETag 失败: %v", err), traceID, startTime)
			return true
		}
	}

	// 生成文件的 Last-Modified 时间（使用文件的修改时间）
	var fileLastModified time.Time
	if fileInfo, err := os.Stat(localFilePath); err == nil {
		fileLastModified = fileInfo.ModTime()
	} else {
		fileLastModified = getLastModified(fileContent)
	}

	// 检查条件请求（If-None-Match 和 If-Modified-Since）
	if config.etag && fileEtag != "" {
		if checkConditionalRequest(r, fileEtag, fileLastModified) {
			handle304Response(w, r, fileContent, encoding, language, fileEtag, fileLastModified, traceID, method, host, url, startTime)
			return true
		}
	}

	// 使用提取的函数处理请求
	processRequestWithContent(w, r, fileContent, encoding, language, fileEtag, useChunked, traceID, method, host, url, startTime)
	return true
}

func serverHandler(w http.ResponseWriter, r *http.Request) {
	// 准备请求上下文
	traceID, method, host, url, startTime := prepareRequestContext(w, r)

	// 包装 ResponseWriter 以捕获状态码
	wrapper := &responseWriterWrapper{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // 默认状态码
	}

	// 先生成响应体（用于 ETag 生成和条件请求检查）
	responseSize := serverGetRespSize(r)
	responseBody, etag := genRespBody(responseSize)

	// 生成 Last-Modified 时间
	lastModified := getLastModified(responseBody)

	// 检查条件请求（If-None-Match 和 If-Modified-Since）
	// 在 handleLocalFileRequest 之前检查，如果匹配 304 则直接返回
	if config.etag && etag != "" {
		if checkConditionalRequest(r, etag, lastModified) {
			handle304Response(wrapper, r, responseBody, "", "", etag, lastModified, traceID, method, host, url, startTime)
			return
		}
	}

	// 处理 X-Req-Local-File 请求头
	// 注意：如果请求有 X-Req-Local-File 头，会在 handleLocalFileRequest 中重新处理
	// 为了简化，这里先检查条件请求，如果不匹配 304，再处理本地文件
	if handled := handleLocalFileRequest(wrapper, r, responseBody, etag, lastModified, traceID, method, host, url, startTime); handled {
		return
	}

	// 检查请求头是否控制 chunked 传输
	useChunked := parseChunkedHeader(r)

	ae := r.Header.Get("Accept-Encoding")
	encoding := negotiateEncoding(ae)

	al := r.Header.Get("Accept-Language")
	language := negotiateLanguage(al)

	// 使用提取的函数处理请求
	processRequestWithContent(wrapper, r, responseBody, encoding, language, etag, useChunked, traceID, method, host, url, startTime)
}

// generateLocalFileIfNeeded 根据需要生成本地文件
// 如果文件已存在，则跳过生成
func generateLocalFileIfNeeded() {
	if config.localFile == "" {
		return
	}
	
	// 检查文件是否已存在
	if _, err := os.Stat(config.localFile); err == nil {
		log.Printf("文件已存在，跳过生成: %s", config.localFile)
		return
	}
	
	size, err := parseFileSize(config.localFile)
	if err != nil {
		log.Fatalf("解析文件大小失败: %v", err)
	}
	
	if err := generateFile(config.localFile, size); err != nil {
		log.Fatalf("生成文件失败: %v", err)
	}
}

// generateCertIfNeeded 根据需要生成自签证书
func generateCertIfNeeded() {
	if config.generateCert == "" {
		return
	}
	
	if config.certFile == "" {
		config.certFile = "cert.pem"
	}
	if config.keyFile == "" {
		config.keyFile = "key.pem"
	}
	
	if err := generateSelfSignedCert(config.certFile, config.keyFile, config.generateCert); err != nil {
		log.Fatalf("生成自签证书失败: %v", err)
	}
}

// createTLSConfig 创建 TLS 配置
func createTLSConfig(cert tls.Certificate) *tls.Config {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	tlsConfig.Certificates = []tls.Certificate{cert}

	// 控制 SNI 校验
	if !config.enableSNI {
		tlsConfig.InsecureSkipVerify = true
	}

	// 添加 SNI 打印功能
	tlsConfig.GetCertificate = func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		// 打印 SNI 到文件
		go func() {
			f, err := os.OpenFile("/tmp/cache_press.sni.output", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Printf("打开 SNI 输出文件失败: %v", err)
				return
			}
			defer f.Close()
			_, err = fmt.Fprintf(f, "%s\n", clientHello.ServerName)
			if err != nil {
				log.Printf("写入 SNI 到文件失败: %v", err)
			}
		}()
		return &cert, nil
	}

	return tlsConfig
}

// startHTTPServers 启动 HTTP 服务器
func startHTTPServers() {
	// 设置默认端口
	if len(config.ports) == 0 {
		config.ports = []int{9000}
	}

	for _, port := range config.ports {
		httpAddr := fmt.Sprintf(":%d", port)
		if config.listenIP != "" {
			httpAddr = fmt.Sprintf("%s:%d", config.listenIP, port)
		}

		httpServer := &http.Server{
			Addr:              httpAddr,
			ReadHeaderTimeout: 10 * time.Second,
		}

		fmt.Printf("HTTP 服务器监听地址: %s\n", httpAddr)
		go func(addr string, srv *http.Server) {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTP 服务器启动失败 (地址: %s): %v", addr, err)
			}
		}(httpAddr, httpServer)
	}
}

// startHTTPSServers 启动 HTTPS 服务器
func startHTTPSServers(cert tls.Certificate) {
	for _, port := range config.httpsPorts {
		httpsAddr := fmt.Sprintf(":%d", port)
		if config.listenIP != "" {
			httpsAddr = fmt.Sprintf("%s:%d", config.listenIP, port)
		}

		tlsConfig := createTLSConfig(cert)

		httpsServer := &http.Server{
			Addr:              httpsAddr,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         tlsConfig,
		}

		fmt.Printf("HTTPS 服务器监听地址: %s\n", httpsAddr)
		go func(addr string, srv *http.Server) {
			if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("HTTPS 服务器启动失败 (地址: %s): %v", addr, err)
			}
		}(httpsAddr, httpsServer)
	}
}

func startServer() {
	initAccessLog()
	defer closeAccessLog()

	// 生成本地文件（如果需要）
	generateLocalFileIfNeeded()

	// 生成自签证书（如果需要）
	generateCertIfNeeded()

	http.HandleFunc("/", serverHandler)

	// 启动 HTTP 服务器
	startHTTPServers()

	// 启动 HTTPS 服务器
	if len(config.httpsPorts) > 0 {
		if config.certFile == "" || config.keyFile == "" {
			log.Fatalf("启用 HTTPS 时必须指定证书文件和私钥文件，或使用 --generate-cert 生成自签证书")
		}

		// 加载证书
		cert, err := tls.LoadX509KeyPair(config.certFile, config.keyFile)
		if err != nil {
			log.Fatalf("加载证书失败: %v", err)
		}

		startHTTPSServers(cert)

		// 启用了 HTTPS 服务器，需要阻塞
		select {}
	} else {
		// 如果只启动了 HTTP 服务器，需要阻塞
		select {}
	}
}
