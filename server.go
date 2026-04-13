package main

import (
	"bytes"
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
	"io"
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

var (
	respCache      = make(map[int]respCacheItem)
	respCacheMutex sync.RWMutex
)

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
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 创建文件
	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer f.Close()

	// 生成文件内容
	// 使用 1MB 的缓冲区
	bufferSize := int64(1024 * 1024)
	buffer := make([]byte, bufferSize)

	// 填充缓冲区
	for i := range buffer {
		buffer[i] = 'x'
	}

	// 写入文件
	written := int64(0)
	for written < size {
		// 计算本次写入的大小
		writeSize := bufferSize
		if written+writeSize > size {
			writeSize = size - written
		}

		// 写入数据
		n, err := f.Write(buffer[:writeSize])
		if err != nil {
			return fmt.Errorf("写入文件失败: %w", err)
		}

		written += int64(n)
	}

	// 确保文件大小正确
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("截断文件失败: %w", err)
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

func serverHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	serveHeaderWithDelay()

	traceID := getTraceID(r)
	method := r.Method
	host := r.Host
	url := r.URL.String()

	// 处理 X-Req-Local-File 请求头
	if localFilePath := r.Header.Get("X-Req-Local-File"); localFilePath != "" {
		// 包装 ResponseWriter 以捕获状态码
		wrapper := &responseWriterWrapper{
			ResponseWriter: w,
			statusCode:     http.StatusOK, // 默认状态码
		}

		// 检查文件是否存在
		if _, err := os.Stat(localFilePath); os.IsNotExist(err) {
			http.Error(wrapper, fmt.Sprintf("文件不存在: %s", localFilePath), http.StatusNotFound)
			logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, nil)
			return
		}

		// 打开文件
		file, err := os.Open(localFilePath)
		if err != nil {
			http.Error(wrapper, fmt.Sprintf("打开文件失败: %v", err), http.StatusInternalServerError)
			logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, err)
			return
		}
		defer file.Close()

		// 获取文件信息
		fileInfo, err := file.Stat()
		if err != nil {
			http.Error(wrapper, fmt.Sprintf("获取文件信息失败: %v", err), http.StatusInternalServerError)
			logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, err)
			return
		}

		// 设置响应头
		wrapper.Header().Set("Content-Type", "application/octet-stream")
		wrapper.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))
		wrapper.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(localFilePath)))

		// 检查是否使用 chunked 传输
		if chunkedHeader := r.Header.Get("X-Use-Chunked-Transfer"); chunkedHeader != "" {
			if chunkedHeader == "true" || chunkedHeader == "1" {
				wrapper.Header().Set("Transfer-Encoding", "chunked")
				wrapper.Header().Del("Content-Length")
			} else if chunkedHeader == "false" || chunkedHeader == "0" {
				// 确保使用 Content-Length
				wrapper.Header().Del("Transfer-Encoding")
				wrapper.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))
			}
		} else if config.useChunkedTransfer {
			// 使用配置的 chunked 传输
			wrapper.Header().Set("Transfer-Encoding", "chunked")
			wrapper.Header().Del("Content-Length")
		}

		// 发送响应头
		wrapper.WriteHeader(http.StatusOK)

		// 发送文件内容
		if config.sendBytesPerInterval > 0 && config.sendIntervalMs > 0 {
			// 限速发送
			buffer := make([]byte, config.sendBytesPerInterval)
			for {
				n, err := file.Read(buffer)
				if n > 0 {
					wrapper.Write(buffer[:n])
					time.Sleep(time.Duration(config.sendIntervalMs) * time.Millisecond)
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(wrapper, fmt.Sprintf("读取文件失败: %v", err), http.StatusInternalServerError)
					logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, err)
					return
				}
			}
		} else {
			// 直接发送文件
			if _, err := io.Copy(wrapper, file); err != nil {
				http.Error(wrapper, fmt.Sprintf("发送文件失败: %v", err), http.StatusInternalServerError)
				logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, err)
				return
			}
		}

		// 记录访问日志
		logAccess(traceID, r, wrapper, startTime, 0, wrapper.statusCode, nil)
		return
	}

	// 包装 ResponseWriter 以捕获状态码
	wrapper := &responseWriterWrapper{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // 默认状态码
	}

	responseSize := serverGetRespSize(r)
	responseBody, etag := genRespBody(responseSize)

	// 检查请求头是否控制 chunked 传输
	useChunked := config.useChunkedTransfer
	if chunkedHeader := r.Header.Get("X-Use-Chunked-Transfer"); chunkedHeader != "" {
		if chunkedHeader == "true" || chunkedHeader == "1" {
			useChunked = true
		} else if chunkedHeader == "false" || chunkedHeader == "0" {
			useChunked = false
		}
	}

	ae := r.Header.Get("Accept-Encoding")
	var encoding string
	if ae != "" {
		if bytes.Contains([]byte(ae), []byte("br")) {
			encoding = "br"
		} else if bytes.Contains([]byte(ae), []byte("gzip")) {
			encoding = "gzip"
		}
	}

	// 处理 X-Mock-302-Location-Map 请求头 - 返回 302 重定向
	if locationMap := r.Header.Get("X-Mock-302-Location-Map"); locationMap != "" {
		handled := handleMock302Redirect(wrapper, r, locationMap, traceID, method, host, url, startTime)
		if handled {
			return
		}
	}

	// 处理 X-Mock-Resp-Code 请求头 - 返回自定义状态码响应
	if mockRespCode := r.Header.Get("X-Mock-Resp-Code"); mockRespCode != "" {
		handleMockResponse(wrapper, r, responseBody, encoding, etag, traceID, method, host, url, startTime, mockRespCode)
		return
	}

	if method == "HEAD" {
		handleHeadResponse(wrapper, r, responseBody, encoding, etag, traceID, method, host, url, startTime)
		return
	}

	if config.preCompress && encoding != "" {
		if r.Header.Get("Range") != "" {
			handlePreCompressedRange(wrapper, r, responseBody, encoding, etag, traceID, method, host, url, startTime)
		} else {
			handlePreCompressedResponse(wrapper, r, responseBody, encoding, etag, traceID, method, host, url, startTime)
		}
		return
	}

	if r.Header.Get("Range") != "" {
		handleRangeRequest(wrapper, r, responseBody, etag, traceID, method, host, url, startTime)
		return
	}

	handleNormalResponse(wrapper, r, responseBody, encoding, etag, useChunked, traceID, method, host, url, startTime)
}

func startServer() {
	initAccessLog()
	defer closeAccessLog()

	// 处理本地文件生成
	if config.localFile != "" {
		size, err := parseFileSize(config.localFile)
		if err != nil {
			log.Fatalf("解析文件大小失败: %v", err)
		}
		if err := generateFile(config.localFile, size); err != nil {
			log.Fatalf("生成文件失败: %v", err)
		}
	}

	// 处理自签证书生成
	if config.generateCert != "" {
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

	http.HandleFunc("/", serverHandler)

	// 设置默认端口
	if len(config.ports) == 0 {
		config.ports = []int{9000}
	}

	// 启动 HTTP 服务器
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

		for _, port := range config.httpsPorts {
			httpsAddr := fmt.Sprintf(":%d", port)
			if config.listenIP != "" {
				httpsAddr = fmt.Sprintf("%s:%d", config.listenIP, port)
			}

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

		// 启用了 HTTPS 服务器，需要阻塞
		select {}
	} else {
		// 如果只启动了 HTTP 服务器，需要阻塞
		select {}
	}
}
