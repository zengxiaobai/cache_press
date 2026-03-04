package main

import (
	"bytes"
	"cache_press/pkg/buffer"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

var (
	respCache      = make(map[int][]byte)
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

func genRespBody(responseSize int) []byte {
	var responseBody []byte
	if config.cacheResp {
		respCacheMutex.RLock()
		var ok bool
		responseBody, ok = respCache[responseSize]
		respCacheMutex.RUnlock()
		if !ok {
			newBody := createRespBodyCont(responseSize)
			respCacheMutex.Lock()
			defer respCacheMutex.Unlock()
			if _, ok := respCache[responseSize]; !ok {
				respCache[responseSize] = newBody
			}
			responseBody = newBody
		}
	} else {
		responseBody = createRespBodyCont(responseSize)
	}
	return responseBody
}

func serverHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	serveHeaderWithDelay()

	traceID := getTraceID(r)
	method := r.Method
	host := r.Host
	url := r.URL.String()

	responseSize := serverGetRespSize(r)
	responseBody := genRespBody(responseSize)

	ae := r.Header.Get("Accept-Encoding")
	var encoding string
	if ae != "" {
		if bytes.Contains([]byte(ae), []byte("br")) {
			encoding = "br"
		} else if bytes.Contains([]byte(ae), []byte("gzip")) {
			encoding = "gzip"
		}
	}

	// 处理 X-Mock-304 请求头 - 返回 304 响应但保持响应头不变
	if r.Header.Get("X-Mock-304") != "" {
		handle304Response(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		return
	}

	if method == "HEAD" {
		handleHeadResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		return
	}

	if config.preCompress && encoding != "" {
		if r.Header.Get("Range") != "" {
			handlePreCompressedRange(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		} else {
			handlePreCompressedResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
		}
		return
	}

	if r.Header.Get("Range") != "" {
		handleRangeRequest(w, r, responseBody, traceID, method, host, url, startTime)
		return
	}

	handleNormalResponse(w, r, responseBody, encoding, traceID, method, host, url, startTime)
}

func startServer() {
	initAccessLog()
	defer closeAccessLog()

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

	// 启动 HTTP 服务器
	httpAddr := fmt.Sprintf(":%d", config.port)
	if config.listenIP != "" {
		httpAddr = fmt.Sprintf("%s:%d", config.listenIP, config.port)
	}

	httpServer := &http.Server{
		Addr:              httpAddr,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("HTTP 服务器监听地址: %s\n", httpAddr)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务器启动失败: %v", err)
		}
	}()

	// 启动 HTTPS 服务器
	if config.httpsPort > 0 {
		if config.certFile == "" || config.keyFile == "" {
			log.Fatalf("启用 HTTPS 时必须指定证书文件和私钥文件，或使用 --generate-cert 生成自签证书")
		}

		httpsAddr := fmt.Sprintf(":%d", config.httpsPort)
		if config.listenIP != "" {
			httpsAddr = fmt.Sprintf("%s:%d", config.listenIP, config.httpsPort)
		}

		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}

		// 加载证书
		cert, err := tls.LoadX509KeyPair(config.certFile, config.keyFile)
		if err != nil {
			log.Fatalf("加载证书失败: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}

		// 控制 SNI 校验
		if !config.enableSNI {
			tlsConfig.InsecureSkipVerify = true
			tlsConfig.GetCertificate = func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				return &cert, nil
			}
		}

		httpsServer := &http.Server{
			Addr:              httpsAddr,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         tlsConfig,
		}

		fmt.Printf("HTTPS 服务器监听地址: %s\n", httpsAddr)
		fmt.Printf("启用 HTTPS，证书文件: %s, 私钥文件: %s\n", config.certFile, config.keyFile)
		fmt.Printf("SNI 校验: %v\n", config.enableSNI)

		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTPS 服务器启动失败: %v", err)
		}
	} else {
		// 如果只启动了 HTTP 服务器，需要阻塞
		select {}
	}
}
