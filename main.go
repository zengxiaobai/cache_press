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
	"sync/atomic"
	"syscall"

	//	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Config struct {
	mode       string
	port       int
	host       string
	addr       string
	conns      int
	qps        int
	duration   time.Duration
	tickerDump time.Duration

	// 响应大小配置 - 仅客户端使用
	respSizeStr   string
	respSizeRange []int
	diskRatio     float64

	// CDN命中率配置 - 仅客户端使用
	hitRatio            float64
	urlCount            int
	ignoreErr           bool
	deferStart          int
	delayRespHdr        int
	delayRespHdrRandom  int
	delayRespBody       int
	delayRespBodyRandom int

	ReqIDHdrName string
	chunkResp    float64
	CloseConn    float64

	// 响应体缓存配置 - 仅服务器使用
	cacheResp bool

	// MD5校验配置 - 仅服务器使用
	enableMD5 bool

	// 测试MD5校验失败 - 仅客户端使用
	testMD5Failure bool

	// 持久连接控制 - 仅服务器使用
	keepAliveProb          float64 // Connection头为keep-alive的概率 (0.0-1.0)
	closeConnAfterBodyProb float64 // 发完body后主动关闭连接的概率 (0.0-1.0)

	// 连接池配置 - 仅客户端使用
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration

	// 客户端主动断开连接控制
	clientSendCloseProb     float64 // 发送完请求后主动断开连接的概率 (0.0-1.0)
	clientRecvHalfCloseProb float64 // 接收响应body一半时主动断开连接的概率 (0.0-1.0)
	clientRecvFullCloseProb float64 // 接收完响应后主动断开连接的概率 (0.0-1.0)
}

type reqStatInfo struct {
	respTime      time.Duration
	firstByteTime time.Duration
	cacheHit      bool
}

var config Config
var transport *http.Transport

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
	flag.IntVar(&config.port, "port", 8080, "服务器端口")
	flag.StringVar(&config.host, "host", "localhost", "服务器主机名或IP")
	flag.StringVar(&config.addr, "addr", "", "服务器完整地址 (格式: host:port)，如果设置了此参数则忽略host和port)")
	flag.IntVar(&config.conns, "conns", 10, "并发连接数")
	flag.IntVar(&config.qps, "qps", 100, "QPS限制")
	flag.DurationVar(&config.duration, "duration", 30*time.Second, "压测持续时间")
	flag.DurationVar(&config.tickerDump, "ticker-dump", 5*time.Second, "定时输出统计信息间隔")

	// 响应大小配置 - 仅客户端使用
	flag.StringVar(&config.respSizeStr, "resp-size", "1024", "响应大小，格式: 单个数字或范围 [min,max]")
	flag.Float64Var(&config.diskRatio, "disk-ratio", 0.5, "小响应体比例 (0.0-1.0)")

	// CDN命中率配置 - 仅客户端使用
	flag.Float64Var(&config.hitRatio, "hit-ratio", 0.5, "CDN命中率 (0.0-1.0)")
	flag.IntVar(&config.urlCount, "url-count", 1000000, "总URL数量")
	flag.BoolVar(&config.ignoreErr, "ignore-err", false, "忽略错误")
	flag.IntVar(&config.deferStart, "defer-start", 0, "延迟启动时间(秒)")
	flag.IntVar(&config.delayRespHdr, "delay-resp-hdr", 0, "延迟响应头时间(毫秒)")
	flag.IntVar(&config.delayRespHdrRandom, "delay-resp-hdr-random", 0, "延迟响应头随机时间(毫秒)")
	flag.IntVar(&config.delayRespBody, "delay-resp-body", 0, "延迟响应体时间(毫秒)")
	flag.IntVar(&config.delayRespBodyRandom, "delay-resp-body-random", 0, "延迟响应体随机时间(毫秒)")
	flag.Float64Var(&config.chunkResp, "chunk-resp", 0.0, "分块响应比例 (0.0-1.0)")
	flag.Float64Var(&config.CloseConn, "client-close-conn-prob", 0.0, "请求后关闭连接比例 (0.0-1.0)")
	flag.StringVar(&config.ReqIDHdrName, "req-id-hdr-name", "X-Request-ID", "请求ID头名称")
	flag.BoolVar(&config.cacheResp, "cache-resp", true, "启用响应体缓存 (仅服务器模式)")
	flag.BoolVar(&config.enableMD5, "enable-md5", false, "启用MD5校验 (仅服务器模式)")
	flag.BoolVar(&config.testMD5Failure, "test-md5-failure", false, "测试MD5校验失败 (仅客户端模式)")

	// 连接池配置 - 仅客户端使用
	flag.IntVar(&config.maxIdleConns, "max-idle-conns", 2000, "最大空闲连接数")
	flag.IntVar(&config.maxIdleConnsPerHost, "max-idle-conns-per-host", 1000, "每个主机最大空闲连接数")
	flag.DurationVar(&config.idleConnTimeout, "idle-conn-timeout", 100*time.Second, "空闲连接超时时间")

	// 持久连接控制 - 仅服务器使用
	flag.Float64Var(&config.keepAliveProb, "server-keep-alive-prob", 1.0, "Connection头为keep-alive的概率 (0.0-1.0)")
	flag.Float64Var(&config.closeConnAfterBodyProb, "server-close-conn-after-body-prob", 0.0, "发完body后主动关闭连接的概率 (0.0-1.0)")

	// 客户端主动断开连接控制
	flag.Float64Var(&config.clientSendCloseProb, "client-send-close-prob", 0.0, "发送完请求后主动断开连接的概率 (0.0-1.0)")
	flag.Float64Var(&config.clientRecvHalfCloseProb, "client-recv-half-close-prob", 0.0, "接收响应body一半时主动断开连接的概率 (0.0-1.0)")
	flag.Float64Var(&config.clientRecvFullCloseProb, "client-recv-full-close-prob", 0.0, "接收完响应后主动断开连接的概率 (0.0-1.0)")
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
	return fmt.Sprintf("%s/path%d.js", baseURL, id)
}

var id, notHitID int64

func incrID() int64 {
	return atomic.AddInt64(&id, 1)
}

func getID() int64 {
	return atomic.LoadInt64(&id)
}

func incrNotHitID() int64 {
	return atomic.AddInt64(&notHitID, 1)
}

func generateRandomURL(baseURL string, urlCount int, hitRatio float64) string {

	// 根据命中率决定是否使用已访问过的URL
	id := getID()
	if rand.Float64() <= hitRatio && id > 0 {
		// 从已访问的URL中随机选择一个
		randIndex := rand.Intn(int(id))
		return genURL(baseURL, int64(randIndex))
	}

	if getID() < int64(urlCount) {
		// 生成新的随机URL
		newURL := fmt.Sprintf("%s/path%d.js", baseURL, incrID())
		return newURL

	} else {
		return fmt.Sprintf("%s/path%d_nocache_%d.js", baseURL, rand.Intn(urlCount*2), incrNotHitID())
	}

}

func main() {
	flag.Parse()

	switch config.mode {
	case "server":
		startServer()
	case "client":
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
