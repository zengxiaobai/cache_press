package main

import (
	//	"bufio"
	"bytes"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sync/atomic"
	"syscall"

	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Config struct {
	mode       string
	ports      []int // HTTP 端口列表（默认 [9000]）
	host       string
	addr       string
	conns      int
	qps        int
	duration   time.Duration
	tickerDump time.Duration

	// pprof 配置
	pprofPort int

	// 响应大小配置 - 仅客户端使用
	respSizeStr   string
	respSizeRange []int
	diskRatio     float64

	// CDN命中率配置 - 仅客户端使用
	hitRatio            float64
	urlCount            int
	fixedURLStr         string   // 固定 URL 列表字符串 (仅客户端使用，URI格式，不含host)
	fixedURLs           []string // 固定 URL 列表 (仅客户端使用，URI格式，不含host)
	urlSuffix           string   // URL 后缀 (仅客户端使用，默认为 .js)
	maxRequests         int      // 最大请求数量 (仅客户端使用，0表示不限制)
	compareAddr         string   // Range 请求时用于对比 hash 的地址 (仅客户端使用)
	ignoreErr           bool
	deferStart          int
	delayRespHdr        int
	delayRespHdrRandom  int
	delayRespBody       int
	delayRespBodyRandom int

	// Range 请求配置 - 仅客户端使用
	enableRange bool   // 是否启用 Range 请求
	rangeStr    string // Range 配置字符串，格式: "[0-2048,2049-5000]"
	rangeRandom bool   // 是否在每个 range 上下限之间随机

	ReqIDHdrName string
	chunkResp    float64
	CloseConn    float64
	logDir       string
	listenIP     string

	// HTTPS 配置 - 仅服务器使用
	httpsPorts   []int  // HTTPS 端口列表（默认 []，表示不启用 HTTPS）
	certFile     string // 证书文件路径
	keyFile      string // 私钥文件路径
	generateCert string // 生成自签证书的域名（为空表示不生成）
	enableSNI    bool   // 是否启用 SNI 校验（默认 false）

	// 响应体缓存配置 - 仅服务器使用
	cacheResp bool

	// etag 配置 - 仅服务器使用
	etag bool

	// 响应体内容配置 - 仅服务器使用
	useRandomContent bool // 是否使用随机内容生成响应体 (默认 false，使用重复模式)

	// 哈希校验配置 - 仅服务器使用
	enableHash bool

	// 日志配置 - 仅服务器使用
	logRequestHeaders  bool // 是否打印请求头
	logResponseHeaders bool // 是否打印响应头

	// Multi Range 传输方式配置 - 仅服务器使用
	multiRangeChunked bool // multi range 是否使用 chunked 传输 (默认 false，使用 Content-Length)

	// 预压缩配置 - 仅服务器使用
	preCompress bool // 是否预压缩整个文件后再支持 Range (类似 Nginx 的 gzip_static)

	// 测试哈希校验失败 - 仅客户端使用
	testHashFailure bool

	// 持久连接控制 - 仅服务器使用
	keepAliveProb          float64 // Connection头为keep-alive的概率 (0.0-1.0)
	closeConnAfterBodyProb float64 // 发完body后主动关闭连接的概率 (0.0-1.0)

	// 发送速率控制 - 仅服务器使用
	sendBytesPerInterval int      // 每次发送的字节数
	sendIntervalMs       int      // 每次发送后的 sleep 时间 (毫秒)
	respRate             string   // 响应速率限制，格式: "10MB/s" 或 "100KB/s"
	respHeaderFile       string   // 响应头文件路径 (仅服务器模式)
	respHeaders          []string // 解析后的响应头列表 (仅服务器模式)
	cmdRespHeaders       []string // 命令行指定的响应头列表 (仅服务器模式)
	useChunkedTransfer   bool     // 是否使用 chunked 传输 (默认 false，使用 Content-Length)
	vary                 string   // Vary 头配置字符串，格式: "[\"Accept-Encoding\",\"User-Agent\"]"
	varyHeaders          []string // 解析后的 Vary 头列表

	// 连接池配置 - 仅客户端使用
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration

	// 客户端主动断开连接控制
	clientSendCloseProb     float64  // 发送完请求后主动断开连接的概率 (0.0-1.0)
	clientRecvHalfCloseProb float64  // 接收响应body一半时主动断开连接的概率 (0.0-1.0)
	clientRecvFullCloseProb float64  // 接收完响应后主动断开连接的概率 (0.0-1.0)
	addHeaderFile           string   // 请求头配置文件路径 (仅客户端使用)
	customHeaders           []string // 解析后的自定义请求头列表
}

type reqStatInfo struct {
	respTime      time.Duration
	firstByteTime time.Duration
	cacheHit      bool
	traceID       string
}

var config Config
var transport *http.Transport
var defaultRespSize int // 默认响应大小（服务器模式）

var reqStatCh chan reqStatInfo

func initTransport() {
	// 创建自定义 Transport
	transport = &http.Transport{
		MaxIdleConns:        config.maxIdleConns,        // 创建 Transport 时设置最大空闲连接数
		MaxIdleConnsPerHost: config.maxIdleConnsPerHost, // 创建 Transport 时设置每个主机最大空闲连接数
		IdleConnTimeout:     config.idleConnTimeout,     // 创建 Transport 时设置空闲连接超时
		DisableCompression:  true,                       // 禁用自动添加Accept-Encoding头和自动解压缩
	}
	customDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			var operr error
			err := c.Control(func(fd uintptr) {
				// 设置 SO_LINGER 为0，实现优雅关闭
				linger := &unix.Linger{
					Onoff:  1,
					Linger: 0,
				}
				operr = unix.SetsockoptLinger(int(fd), unix.SOL_SOCKET, unix.SO_LINGER, linger)
				if operr != nil {
					return
				}

				// 设置 IP_BIND_ADDRESS_NO_PORT (Linux 4.2+)
				// 这个选项允许绑定地址时不预留端口
				operr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BIND_ADDRESS_NO_PORT, 1)
			})

			if err != nil {
				return err
			}
			return operr
		},
	}

	// 更新 transport 使用自定义 dialer
	transport.DialContext = customDialer.DialContext
}

func init() {
	flag.StringVar(&config.mode, "mode", "server", "运行模式: server/client")
	flag.Func("port", "服务器端口（可多次指定）", func(value string) error {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("无效的端口号: %s", value)
		}
		config.ports = append(config.ports, port)
		return nil
	})
	flag.StringVar(&config.host, "host", "localhost", "服务器主机名或IP")
	flag.StringVar(&config.addr, "addr", "", "服务器完整地址 (格式: host:port)，如果设置了此参数则忽略host和port)")
	flag.IntVar(&config.conns, "conns", 10, "并发连接数")
	flag.IntVar(&config.qps, "qps", 100, "QPS限制")
	flag.DurationVar(&config.duration, "duration", 30*time.Second, "压测持续时间")
	flag.DurationVar(&config.tickerDump, "ticker-dump", 5*time.Second, "定时输出统计信息间隔")
	flag.IntVar(&config.pprofPort, "pprof-port", 0, "pprof 监听端口 (0 表示不启用)")

	// 响应大小配置 - 仅客户端使用
	flag.StringVar(&config.respSizeStr, "resp-size", "1024", "响应大小，格式: 单个数字或范围 [min,max]")
	flag.Float64Var(&config.diskRatio, "disk-ratio", 0.5, "小响应体比例 (0.0-1.0)")

	// CDN命中率配置 - 仅客户端使用
	flag.Float64Var(&config.hitRatio, "hit-ratio", 0.5, "CDN命中率 (0.0-1.0)")
	flag.IntVar(&config.urlCount, "url-count", 1000000, "总URL数量")
	flag.StringVar(&config.fixedURLStr, "fixed-url", "", "固定 URL 列表 (仅客户端模式，URI格式，不含host，多个用逗号分隔)")
	flag.StringVar(&config.urlSuffix, "url-suffix", ".js", "URL 后缀 (仅客户端模式，默认为 .js)")
	flag.IntVar(&config.maxRequests, "max-requests", 0, "最大请求数量 (仅客户端模式，0表示不限制)")
	flag.BoolVar(&config.ignoreErr, "ignore-err", false, "忽略错误")
	flag.IntVar(&config.deferStart, "defer-start", 0, "延迟启动时间(秒)")
	flag.IntVar(&config.delayRespHdr, "delay-resp-hdr", 0, "延迟响应头时间(毫秒)")
	flag.IntVar(&config.delayRespHdrRandom, "delay-resp-hdr-random", 0, "延迟响应头随机时间(毫秒)")
	flag.IntVar(&config.delayRespBody, "delay-resp-body", 0, "延迟响应体时间(毫秒)")
	flag.IntVar(&config.delayRespBodyRandom, "delay-resp-body-random", 0, "延迟响应体随机时间(毫秒)")
	flag.Float64Var(&config.chunkResp, "chunk-resp", 0.0, "分块响应比例 (0.0-1.0)")
	flag.Float64Var(&config.CloseConn, "client-close-conn-prob", 0.0, "请求后关闭连接比例 (0.0-1.0)")

	// Range 请求配置 - 仅客户端使用
	flag.StringVar(&config.rangeStr, "range", "", "启用 Range 请求 (仅客户端模式)，格式: -range \"[0-2048,2049-5000]\"")
	flag.BoolVar(&config.rangeRandom, "range-random", false, "在每个 range 上下限之间随机 (仅客户端模式)")
	flag.StringVar(&config.ReqIDHdrName, "req-id-hdr-name", "X-WYCDN-Request-ID", "请求ID头名称")
	flag.StringVar(&config.logDir, "log-dir", "", "访问日志文件路径")
	flag.StringVar(&config.listenIP, "listen-ip", "", "服务器监听IP (默认: 所有网卡)")
	flag.BoolVar(&config.cacheResp, "cache-resp", true, "启用响应体缓存 (仅服务器模式)")
	flag.BoolVar(&config.etag, "etag", false, "是否根据响应内容生成 etag 头 (仅服务器模式)")
	flag.BoolVar(&config.useRandomContent, "random-content", false, "使用随机内容生成响应体 (仅服务器模式，默认 false 使用重复模式)")
	flag.BoolVar(&config.enableHash, "enable-hash", false, "启用哈希校验 (仅服务器模式)")
	flag.BoolVar(&config.multiRangeChunked, "multi-range-chunked", false, "multi range 使用 chunked 传输 (仅服务器模式，默认 false 使用 Content-Length)")
	flag.BoolVar(&config.preCompress, "pre-compress", false, "预压缩整个文件后再支持 Range (仅服务器模式，类似 Nginx 的 gzip_static)")
	flag.BoolVar(&config.testHashFailure, "test-hash-failure", false, "测试哈希校验失败 (仅客户端模式)")
	// 日志配置 - 仅服务器使用
	flag.BoolVar(&config.logRequestHeaders, "log-request-headers", false, "是否打印请求头 (仅服务器模式)")
	flag.BoolVar(&config.logResponseHeaders, "log-response-headers", false, "是否打印响应头 (仅服务器模式)")

	// HTTPS 配置 - 仅服务器使用
	flag.Func("https-port", "HTTPS 端口 (仅服务器模式，可多次指定，默认不启用 HTTPS)", func(value string) error {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("无效的端口号: %s", value)
		}
		config.httpsPorts = append(config.httpsPorts, port)
		return nil
	})
	flag.StringVar(&config.certFile, "cert-file", "", "证书文件路径 (仅服务器模式，启用 HTTPS 时必需，除非使用 --generate-cert)")
	flag.StringVar(&config.keyFile, "key-file", "", "私钥文件路径 (仅服务器模式，启用 HTTPS 时必需，除非使用 --generate-cert)")
	flag.StringVar(&config.generateCert, "generate-cert", "", "生成自签证书的域名 (仅服务器模式，为空表示不生成)")
	flag.BoolVar(&config.enableSNI, "enable-sni", false, "是否启用 SNI 校验 (仅服务器模式，默认 false)")

	// 连接池配置 - 仅客户端使用
	flag.IntVar(&config.maxIdleConns, "max-idle-conns", 2000, "最大空闲连接数")
	flag.IntVar(&config.maxIdleConnsPerHost, "max-idle-conns-per-host", 1000, "每个主机最大空闲连接数")
	flag.DurationVar(&config.idleConnTimeout, "idle-conn-timeout", 100*time.Second, "空闲连接超时时间")

	// 持久连接控制 - 仅服务器使用
	flag.Float64Var(&config.keepAliveProb, "server-keep-alive-prob", 1.0, "Connection头为keep-alive的概率 (0.0-1.0)")
	flag.Float64Var(&config.closeConnAfterBodyProb, "server-close-conn-after-body-prob", 0.0, "发完body后主动关闭连接的概率 (0.0-1.0)")

	// 发送速率控制 - 仅服务器使用
	flag.IntVar(&config.sendBytesPerInterval, "send-bytes-per-interval", 0, "每次发送的字节数 (仅服务器模式，0表示不限制)")
	flag.IntVar(&config.sendIntervalMs, "send-interval-ms", 0, "每次发送后的 sleep 时间 (毫秒，仅服务器模式)")
	flag.StringVar(&config.respRate, "resp-rate", "", "响应速率限制 (仅服务器模式，格式: \"10MB/s\" 或 \"100KB/s\")")
	flag.StringVar(&config.respHeaderFile, "resp-header-file", "", "响应头文件路径 (仅服务器模式，格式: 每行一个头和值，头跟值中间用空格分开)")
	flag.BoolVar(&config.useChunkedTransfer, "use-chunked-transfer", false, "是否使用 chunked 传输 (仅服务器模式，默认 false 使用 Content-Length)")
	flag.StringVar(&config.vary, "vary", "", "Vary 响应头配置 (仅服务器模式，格式: [\"header1\",\"header2\"])")

	// 客户端主动断开连接控制
	flag.Float64Var(&config.clientSendCloseProb, "client-send-close-prob", 0.0, "发送完请求后主动断开连接的概率 (0.0-1.0)")
	flag.Float64Var(&config.clientRecvHalfCloseProb, "client-recv-half-close-prob", 0.0, "接收响应body一半时主动断开连接的概率 (0.0-1.0)")
	flag.Float64Var(&config.clientRecvFullCloseProb, "client-recv-full-close-prob", 0.0, "接收完响应后主动断开连接的概率 (0.0-1.0)")
	flag.StringVar(&config.addHeaderFile, "req-header-file", "", "自定义请求头文件路径 (仅客户端模式，格式: 每行 header: value)")
	flag.Func("add-resp-header", "添加响应头 (仅服务器模式，格式: \"Header: Value\"，可多次指定)", func(value string) error {
		config.cmdRespHeaders = append(config.cmdRespHeaders, value)
		return nil
	})
}

func parseRespSize(respSizeStr string) []int {
	if strings.Contains(respSizeStr, "[") && strings.Contains(respSizeStr, "]") {
		// 解析范围格式 [min,max]
		respSizeStr = strings.Trim(respSizeStr, "[]")
		parts := strings.Split(respSizeStr, ",")
		if len(parts) == 2 {
			min, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
			max, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err1 == nil && err2 == nil {
				return []int{min, max}
			}
		}
	} else {
		// 单个数值
		size, err := strconv.Atoi(respSizeStr)
		if err == nil {
			return []int{size}
		}
	}
	log.Fatal("无效的响应大小参数格式，应为单个数字或 [min,max] 格式")
	return nil
}

func getRandomResponse(sizeRange []int, ratio float64) []byte {
	if len(sizeRange) == 1 {
		// 固定大小
		return bytes.Repeat([]byte("x"), sizeRange[0])
	}

	// 范围随机，按比例分配
	minSize, maxSize := sizeRange[0], sizeRange[1]
	if rand.Float64() <= ratio {
		return bytes.Repeat([]byte("x"), minSize)
	} else {
		return bytes.Repeat([]byte("x"), maxSize)
	}
}

func genURL(baseURL string, id int64) string {
	return fmt.Sprintf("%s/path%d%s", baseURL, id, config.urlSuffix)
}

var id, notHitID int64

func incrID() int64 {
	return atomic.AddInt64(&id, 1)
}

func getID() int64 {
	return atomic.LoadInt64(&id)
}

func getNotHitID() int64 {
	return atomic.LoadInt64(&notHitID)
}

func getTotalIDs() int64 {
	return getID() + getNotHitID()
}
func incrNotHitID() int64 {
	return atomic.AddInt64(&notHitID, 1)
}

// parseVary 解析 Vary 头配置字符串
// 格式: ["Accept-Encoding","User-Agent"]
func parseVary(varyStr string) []string {
	if varyStr == "" {
		return nil
	}

	// 去除前后的方括号
	varyStr = strings.Trim(varyStr, "[]")
	if varyStr == "" {
		return nil
	}

	// 按逗号分割
	parts := strings.Split(varyStr, ",")
	var headers []string
	for _, part := range parts {
		// 去除前后的引号和空格
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			headers = append(headers, part)
		}
	}
	return headers
}

// parseHeaderFile 解析请求头文件
// 文件格式: 每行 header: value
func parseHeaderFile(filePath string) ([]string, error) {
	if filePath == "" {
		return nil, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取请求头文件失败: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var headers []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 查找冒号分隔符
		colonIndex := strings.Index(line, ":")
		if colonIndex == -1 {
			continue
		}

		headerName := strings.TrimSpace(line[:colonIndex])
		headerValue := strings.TrimSpace(line[colonIndex+1:])
		if headerName != "" {
			headers = append(headers, headerName, headerValue)
		}
	}

	return headers, nil
}

// parseRespRate 解析响应速率字符串
// 格式: "10MB/s" 或 "100KB/s"
// 返回: (sendBytesPerInterval, sendIntervalMs, error)
func parseRespRate(rateStr string) (int, int, error) {
	if rateStr == "" {
		return 0, 0, nil
	}

	// 去除空格
	rateStr = strings.TrimSpace(rateStr)

	// 查找单位分隔符
	unitIndex := strings.IndexFunc(rateStr, func(r rune) bool {
		return !('0' <= r && r <= '9' || r == '.')
	})

	if unitIndex == -1 {
		return 0, 0, fmt.Errorf("无效的速率格式: %s", rateStr)
	}

	// 解析数值部分
	rateStr = strings.TrimSpace(rateStr)
	numStr := rateStr[:unitIndex]
	unitStr := rateStr[unitIndex:]

	// 解析数值
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("无效的速率数值: %s", numStr)
	}

	// 解析单位
	unitStr = strings.TrimSpace(unitStr)
	unitStr = strings.ToUpper(unitStr)

	// 提取字节单位和时间单位
	var byteUnit string
	var timeUnit string

	if strings.Contains(unitStr, "/") {
		parts := strings.Split(unitStr, "/")
		byteUnit = strings.TrimSpace(parts[0])
		timeUnit = strings.TrimSpace(parts[1])
	} else {
		byteUnit = unitStr
		timeUnit = "S"
	}

	// 转换为字节
	var bytesPerSec float64
	switch byteUnit {
	case "B":
		bytesPerSec = num
	case "KB":
		bytesPerSec = num * 1024
	case "MB":
		bytesPerSec = num * 1024 * 1024
	case "GB":
		bytesPerSec = num * 1024 * 1024 * 1024
	default:
		return 0, 0, fmt.Errorf("无效的字节单位: %s", byteUnit)
	}

	// 验证时间单位
	switch timeUnit {
	case "S", "MS":
		// 有效时间单位
	default:
		return 0, 0, fmt.Errorf("无效的时间单位: %s", timeUnit)
	}

	// 计算每次发送的字节数
	// 我们使用 100ms 的间隔来实现更平滑的速率控制
	sendIntervalMs := 100
	sendBytesPerInterval := int(bytesPerSec * float64(sendIntervalMs) / 1000.0)

	// 确保至少发送1字节
	if sendBytesPerInterval < 1 {
		sendBytesPerInterval = 1
	}

	return sendBytesPerInterval, sendIntervalMs, nil
}

// parseRespHeaderFile 解析响应头文件
// 文件格式: 每行一个头和值，头跟值中间用空格分开
func parseRespHeaderFile(filePath string) ([]string, error) {
	if filePath == "" {
		return nil, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取响应头文件失败: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var headers []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 查找第一个空格分隔符
		spaceIndex := strings.Index(line, " ")
		if spaceIndex == -1 {
			continue
		}

		headerName := strings.TrimSpace(line[:spaceIndex])
		headerValue := strings.TrimSpace(line[spaceIndex:])
		if headerName != "" {
			headers = append(headers, headerName, headerValue)
		}
	}

	return headers, nil
}

// parseCmdRespHeaders 解析命令行指定的响应头列表
// 格式: "Header: Value"
func parseCmdRespHeaders(headers []string) ([]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}

	var result []string
	for _, headerLine := range headers {
		headerLine = strings.TrimSpace(headerLine)
		if headerLine == "" {
			continue
		}

		// 查找冒号分隔符
		colonIndex := strings.Index(headerLine, ":")
		if colonIndex == -1 {
			return nil, fmt.Errorf("无效的响应头格式: %s (应为 \"Header: Value\")", headerLine)
		}

		headerName := strings.TrimSpace(headerLine[:colonIndex])
		headerValue := strings.TrimSpace(headerLine[colonIndex+1:])

		if headerName != "" {
			result = append(result, headerName, headerValue)
		}
	}

	return result, nil
}

func generateRandomURL(baseURL string, urlCount int, hitRatio float64) string {
	if len(config.fixedURLs) > 0 {
		randIndex := rand.Intn(len(config.fixedURLs))
		return config.fixedURLs[randIndex]
	}

	id := getID()
	if rand.Float64() <= hitRatio && id > 0 {
		// 命中时，从已生成的URL中随机选择一个，确保均匀分布
		randIndex := rand.Intn(int(id))
		// 使用哈希函数对索引进行处理，确保hash打散
		hashedIndex := int64(hashIndex(randIndex))
		return genURL(baseURL, hashedIndex)
	}

	if getID() < int64(urlCount) {
		// 生成新URL时，使用哈希函数对ID进行处理，确保hash打散
		newID := incrID()
		hashedID := hashIndex(int(newID))
		newURL := fmt.Sprintf("%s/path%d%s", baseURL, hashedID, config.urlSuffix)
		return newURL
	} else {
		// 生成nocache URL时，也使用哈希函数确保hash打散
		randBase := rand.Intn(urlCount * 2)
		hashedBase := hashIndex(randBase)
		hashedNotHitID := hashIndex(int(incrNotHitID()))
		return fmt.Sprintf("%s/path%d_nocache_%d%s", baseURL, hashedBase, hashedNotHitID, config.urlSuffix)
	}
}

// hashIndex 使用简单的哈希函数对索引进行处理，确保hash打散
func hashIndex(index int) int {
	// 使用斐波那契哈希法，确保分布均匀
	const phi = 0x9E3779B9
	index *= phi
	index ^= index >> 16
	return index & 0x7FFFFFFF // 确保结果为正数
}

func main() {
	flag.Parse()

	// 启动 pprof 服务器
	if config.pprofPort > 0 {
		go func() {
			pprofAddr := fmt.Sprintf(":%d", config.pprofPort)
			fmt.Printf("pprof 服务器已启动，监听地址: %s\n", pprofAddr)

			// 注册 pprof 处理器
			mux := http.NewServeMux()
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

			if err := http.ListenAndServe(pprofAddr, mux); err != nil {
				log.Printf("pprof 服务器启动失败: %v\n", err)
			}
		}()
	}

	config.enableRange = config.rangeStr != ""

	if config.fixedURLStr != "" {
		config.fixedURLs = strings.Split(config.fixedURLStr, ",")
		for i, url := range config.fixedURLs {
			config.fixedURLs[i] = strings.TrimSpace(url)
		}
	}

	// 解析服务端 Vary 头配置
	config.varyHeaders = parseVary(config.vary)

	// 解析响应头文件
	respHeaders, err := parseRespHeaderFile(config.respHeaderFile)
	if err != nil {
		fmt.Printf("警告: 解析响应头文件失败: %v\n", err)
	}
	config.respHeaders = respHeaders

	// 解析命令行指定的响应头
	cmdRespHeaders, err := parseCmdRespHeaders(config.cmdRespHeaders)
	if err != nil {
		log.Fatalf("无效的响应头参数: %v", err)
	}
	// 合并响应头：先加文件中的，再加命令行中的（命令行优先级更高，可覆盖）
	if len(cmdRespHeaders) > 0 {
		if config.respHeaders == nil {
			config.respHeaders = cmdRespHeaders
		} else {
			config.respHeaders = append(config.respHeaders, cmdRespHeaders...)
		}
	}

	// 打印使用的响应头
	if len(config.respHeaders) > 0 {
		fmt.Printf("服务器将使用以下响应头:\n")
		for i := 0; i < len(config.respHeaders); i += 2 {
			if i+1 < len(config.respHeaders) {
				fmt.Printf("  %s: %s\n", config.respHeaders[i], config.respHeaders[i+1])
			}
		}
	}

	// 解析响应速率限制
	if config.respRate != "" {
		sendBytes, sendInterval, err := parseRespRate(config.respRate)
		if err != nil {
			log.Fatalf("无效的响应速率配置: %v", err)
		}
		config.sendBytesPerInterval = sendBytes
		config.sendIntervalMs = sendInterval
		fmt.Printf("响应速率限制已设置: %s (每次发送 %d 字节，间隔 %d 毫秒)\n", config.respRate, sendBytes, sendInterval)
	}

	switch config.mode {
	case "server":
		// 解析服务器模式的响应大小
		respSizeRange := parseRespSize(config.respSizeStr)
		if len(respSizeRange) == 1 {
			defaultRespSize = respSizeRange[0]
		} else if len(respSizeRange) == 2 {
			// 范围模式下，使用最大值作为默认值
			defaultRespSize = respSizeRange[1]
		} else {
			defaultRespSize = 1024
		}
		fmt.Printf("服务器默认响应大小: %d 字节\n", defaultRespSize)
		startServer()
	case "client":
		// 解析客户端请求头文件
		var err error
		config.customHeaders, err = parseHeaderFile(config.addHeaderFile)
		if err != nil {
			log.Fatal(err)
		}

		initTransport()
		reqStatCh = make(chan reqStatInfo, 50000)
		config.respSizeRange = parseRespSize(config.respSizeStr)

		if config.deferStart > 0 {
			time.Sleep(time.Duration(config.deferStart) * time.Second)
		}

		runClient()
	default:
		log.Fatal("无效的模式，应为 server 或 client")
	}
}
