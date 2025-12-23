package main

import (
	//	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"

	//	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/time/rate"
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
	hitRatio   float64
	urlCount   int
	ignoreErr  bool
	deferStart int
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
		MaxIdleConns:        config.conns * 2, // 创建 Transport 时设置最大空闲连接数
		MaxIdleConnsPerHost: config.conns,     // 创建 Transport 时设置每个主机最大空闲连接数
		IdleConnTimeout:     90 * time.Second, // 创建 Transport 时设置空闲连接超时
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
	flag.IntVar(&config.urlCount, "url-count", 1000, "总URL数量")
	flag.BoolVar(&config.ignoreErr, "ignore-err", false, "忽略错误")
	flag.IntVar(&config.deferStart, "defer-start", 0, "延迟启动时间(秒)")
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

func serverHandler(w http.ResponseWriter, r *http.Request) {
	// 获取请求头中的 x-press-size 值
	sizeHeader := r.Header.Get("x-press-size")

	var responseSize int
	if sizeHeader != "" {
		parsedSize, err := strconv.Atoi(sizeHeader)
		if err == nil {
			responseSize = parsedSize
		} else {
			responseSize = 1024 // 默认值
		}
	} else {
		// 如果没有头信息，使用默认值
		responseSize = 1024
	}

	// 生成响应体
	responseBody := bytes.Repeat([]byte("x"), responseSize)
	// 检查是否为 Range 请求
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		// 解析 Range 头部，格式如 "bytes=0-499"
		if strings.HasPrefix(rangeHeader, "bytes=") {
			rangeValue := strings.TrimPrefix(rangeHeader, "bytes=")
			if strings.Contains(rangeValue, "-") {
				parts := strings.Split(rangeValue, "-")
				start, err1 := strconv.ParseInt(parts[0], 10, 64)
				end := int64(responseSize - 1)
				if parts[1] != "" {
					end, err2 := strconv.ParseInt(parts[1], 10, 64)
					if err2 != nil {
						end = int64(responseSize - 1)
					}
					if end >= int64(responseSize) {
						end = int64(responseSize - 1)
					}
				}

				if err1 == nil && start <= end && start < int64(responseSize) {
					// 有效 Range 请求
					rangeBody := responseBody[start : end+1]

					// 设置 Range 响应头
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, responseSize))
					w.Header().Set("Content-Length", strconv.Itoa(len(rangeBody)))
					w.Header().Set("Content-Type", "application/octet-stream")
					w.WriteHeader(http.StatusPartialContent)

					// 发送部分响应
					w.Write(rangeBody)
					fmt.Println("Range响应已发送: bytes length:", len(rangeBody), "range:", rangeHeader, r.URL.RequestURI())
					return
				}
			}
		}
	}

	// 设置响应头
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(responseSize))

	// 发送响应
	w.Write(responseBody)
	fmt.Println("响应已发送: bytes length: ", responseSize, r.URL.RequestURI())
}

func startServer() {
	addr := fmt.Sprintf(":%d", config.port)
	fmt.Printf("启动服务器在端口 %s\n", addr)
	fmt.Printf("服务器将根据请求头 x-press-size 的值返回对应大小的响应体\n")

	http.HandleFunc("/", serverHandler)
	log.Fatal(http.ListenAndServe(addr, nil))
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

func runClient() {
	// 构建目标URL基础
	var baseURL string
	if config.addr != "" {
		// 如果指定了完整地址，则使用该地址
		if !strings.HasPrefix(config.addr, "http") {
			baseURL = fmt.Sprintf("http://%s", config.addr)
		} else {
			baseURL = config.addr
		}
	} else {
		// 否则使用host和port构建地址
		baseURL = fmt.Sprintf("http://%s:%d", config.host, config.port)
	}

	limiter := rate.NewLimiter(rate.Limit(config.qps), config.qps)

	var wg sync.WaitGroup

	// 控制并发连接数
	semaphore := make(chan struct{}, config.conns)

	// 统计变量
	var totalRequests, successRequests, failedRequests int64
	var totalBytes int64
	var cacheHits int64
	var cacheHitRatio float64
	var startTime time.Time
	var totalResponseTimeStat time.Duration
	var totalFirstByteTimeStat time.Duration
	var avgFirstByteTimeStat, avgResponseTimeStat time.Duration
	var minFirstByteTimeStat, maxFirstByteTimeStat time.Duration
	var minResponseTimeStat, maxResponseTimeStat time.Duration
	var round int64

	startTime = time.Now()

	// 结束信号
	done := make(chan bool)

	// 监控协程
	go func() {
		ticker := time.NewTicker(config.tickerDump)
		defer ticker.Stop()
		var cnt int64

		for {
			select {
			case <-done:
				return
			case reqStat := <-reqStatCh:
				// 处理请求统计信息（如果需要）
				cnt++
				totalFirstByteTimeStat += reqStat.firstByteTime
				totalResponseTimeStat += reqStat.respTime
				avgFirstByteTimeStat = time.Duration(int64(totalFirstByteTimeStat) / cnt)
				avgResponseTimeStat = time.Duration(int64(totalResponseTimeStat) / cnt)
				if minFirstByteTimeStat == 0 {
					minFirstByteTimeStat = time.Duration(reqStat.firstByteTime)
				} else {
					minFirstByteTimeStat = time.Duration(math.Min(float64(minFirstByteTimeStat), float64(reqStat.firstByteTime)))
				}

				if minResponseTimeStat == 0 {
					minResponseTimeStat = time.Duration(reqStat.respTime)
				} else {
					minResponseTimeStat = time.Duration(math.Min(float64(minResponseTimeStat), float64(reqStat.respTime)))
				}

				if maxFirstByteTimeStat == 0 {
					maxFirstByteTimeStat = time.Duration(reqStat.firstByteTime)
				} else {
					maxFirstByteTimeStat = time.Duration(math.Max(float64(maxFirstByteTimeStat), float64(reqStat.firstByteTime)))
				}

				if maxResponseTimeStat == 0 {
					maxResponseTimeStat = time.Duration(reqStat.respTime)
				} else {
					maxResponseTimeStat = time.Duration(math.Max(float64(maxResponseTimeStat), float64(reqStat.respTime)))
				}

				if reqStat.cacheHit {
					cacheHits++
					cacheHitRatio = float64(cacheHits) / float64(cnt) * 100
				}

			case <-ticker.C:
				round++
				elapsed := time.Since(startTime).Seconds()
				currentTotal := atomic.LoadInt64(&totalRequests)

				// 计算平均时间指标
				fmt.Printf("统计次%d: 总请求数=%d, 成功=%d, 失败=%d, 总字节数=%d, QPS=%.2f, 已用时=%.2fs, 缓存命中率=%.2f%%\n\n",
					round, currentTotal, successRequests, failedRequests, totalBytes,
					float64(currentTotal)/elapsed, elapsed, cacheHitRatio)
				fmt.Printf("      》》》平均首包时间=%v, 平均响应时间=%v, 最大首包时间=%v, 最大响应时间=%v 最小首包时间=%v, 最小响应时间=%v\n\n\n\n",
					avgFirstByteTimeStat, avgResponseTimeStat, maxFirstByteTimeStat, maxResponseTimeStat, minFirstByteTimeStat, minResponseTimeStat)

				totalFirstByteTimeStat = 0
				totalResponseTimeStat = 0
				cnt = 0
				minFirstByteTimeStat = 0
				maxFirstByteTimeStat = 0
				minResponseTimeStat = 0
				maxResponseTimeStat = 0
				cacheHits = 0
				cacheHitRatio = 0
			}
		}
	}()

	// 创建多个goroutine模拟并发请求
	for i := 0; i < config.conns; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: transport,
			}

			for {
				// 检查是否到达结束时间
				if time.Since(startTime) >= config.duration {
					break
				}

				// 限制QPS
				limiter.Wait(context.Background())

				// 获取信号量控制并发数
				semaphore <- struct{}{}

				// 生成随机URL
				url := generateRandomURL(baseURL, config.urlCount, config.hitRatio)

				// 根据配置确定请求头中的响应大小
				var reqSize int
				if len(config.respSizeRange) == 1 {
					reqSize = config.respSizeRange[0]
				} else {
					minSize, maxSize := config.respSizeRange[0], config.respSizeRange[1]
					if rand.Float64() <= config.diskRatio {
						reqSize = minSize
					} else {
						reqSize = maxSize
					}
				}

				// 创建请求
				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					<-semaphore
					continue
				}

				// 设置请求头 - 包括x-press-size头
				req.Header.Set("x-press-size", strconv.Itoa(reqSize))
				req.Header.Set("User-Agent", fmt.Sprintf("PressureTestClient-%d", connID))

				req.URL.Host = config.addr
				req.Host = config.host
				// 记录请求开始时间
				requestStartTime := time.Now()
				// 发送请求
				resp, err := client.Do(req)
				if err != nil {
					// 记录失败请求
					fmt.Println(req.URL.RequestURI(), err)
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					<-semaphore
					if !config.ignoreErr {
						os.Exit(1)
					}

					continue
				}
				defer resp.Body.Close()
				// 记录首包时间（收到响应头的时间）
				firstByteTime := time.Since(requestStartTime)

				if resp.StatusCode > 300 {
					fmt.Println(req.URL.RequestURI(), resp.Status)
					atomic.AddInt64(&failedRequests, 1)
					atomic.AddInt64(&totalRequests, 1)
					io.ReadAll(resp.Body)
					// 忽略错误状态码
					<-semaphore
					if !config.ignoreErr {
						os.Exit(1)
					}
					continue
				}

				//fmt.Println(resp.Status, resp.Header)
				// 检查 X-Cache 头判断是否命中缓存
				cacheHit := false
				xCacheHeader := resp.Header.Get("X-Cache")
				if strings.Contains(xCacheHeader, "HIT") {
					cacheHit = true
				}

				// 读取响应体
				body, err := io.ReadAll(resp.Body)

				// 记录完整响应时间（收到完整响应体的时间）
				responseTime := time.Since(requestStartTime)

				select {
				case reqStatCh <- reqStatInfo{
					firstByteTime: firstByteTime,
					respTime:      responseTime,
					cacheHit:      cacheHit,
				}:
				default:
					// Channel 满时丢弃数据，防止阻塞
					fmt.Println("！！！丢弃数据，统计通道已满！！！")
				}

				if err != nil {
					// 记录失败请求
					fmt.Println("read body err :", err, req.URL.Path, len(body), time.Now().Format("2006-01-02 15:04:05.000"), req.Header.Get("User-Agent"))
					if !config.ignoreErr {
						os.Exit(1)
					}
					failedRequests++
				} else {

					// 记录成功请求
					atomic.AddInt64(&successRequests, 1)
					totalBytes += int64(len(body))
				}

				atomic.AddInt64(&totalRequests, 1)
				<-semaphore
			}
		}(i)
	}

	// 等待所有协程完成
	wg.Wait()

	// 停止监控
	done <- true

	// 输出最终统计
	elapsed := time.Since(startTime).Seconds()
	finalTotal := atomic.LoadInt64(&totalRequests)
	finalHits := atomic.LoadInt64(&cacheHits)

	hitRate := 0.0
	if finalTotal > 0 {
		hitRate = float64(finalHits) / float64(finalTotal) * 100
	}

	fmt.Printf("\n=== 最终统计 ===\n")
	fmt.Printf("目标地址: %s\n", baseURL)
	fmt.Printf("响应大小范围: %v, 小响应体比例: %.2f\n", config.respSizeRange, config.diskRatio)
	fmt.Printf("总请求数: %d\n", totalRequests)
	fmt.Printf("成功请求数: %d\n", successRequests)
	fmt.Printf("失败请求数: %d\n", failedRequests)
	fmt.Printf("缓存命中数: %d\n", finalHits)
	fmt.Printf("缓存命中率: %.2f%%\n", hitRate)
	fmt.Printf("总传输字节数: %d\n", totalBytes)
	fmt.Printf("平均QPS: %.2f\n", float64(totalRequests)/elapsed)
	fmt.Printf("成功率: %.2f%%\n", float64(successRequests)/float64(totalRequests)*100)
	fmt.Printf("总耗时: %.2fs\n", elapsed)
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
